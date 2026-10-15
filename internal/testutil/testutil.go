package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/sandbox"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/usage"
	"github.com/csy20/agt/internal/version"
	"github.com/csy20/agt/internal/workspace"
	"github.com/jackc/pgx/v5"
)

const defaultURL = "postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable"

func DatabaseURL() string {
	if v := os.Getenv("AGT_DATABASE_URL"); v != "" {
		return v
	}
	return defaultURL
}

// Open migrates a private schema and holds a session advisory lock so parallel
// packages cannot truncate each other's rows. Cleanup order is LIFO: callers
// that register a worker cancel after Open stop the worker before the pool
// closes and before the lock is released.
func Open(t *testing.T) *store.Store {
	t.Helper()
	url := DatabaseURL()
	dial, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lockConn, err := pgx.Connect(dial, url)
	cancel()
	if err != nil {
		if MissingPrereq(os.Getenv("AGT_DATABASE_URL") != "") {
			t.Fatalf("postgres: %v", err)
		}
		t.Skipf("postgres unreachable: %v", err)
	}
	// One scripted repair batch holds this lock for several minutes. Other
	// packages wait instead of failing the integration job.
	if _, err := lockConn.Exec(context.Background(), `SET lock_timeout = '15min'`); err != nil {
		lockConn.Close(context.Background())
		t.Fatalf("lock timeout: %v", err)
	}
	if _, err := lockConn.Exec(context.Background(), `SELECT pg_advisory_lock(8675309)`); err != nil {
		lockConn.Close(context.Background())
		t.Fatalf("advisory lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lockConn.Exec(context.Background(), `SELECT pg_advisory_unlock(8675309)`)
		lockConn.Close(context.Background())
	})

	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.Reset(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return st
}

func RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func Fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join(RepoRoot(t), "eval", "fixtures", "payrepo")
}

func FixScript(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(RepoRoot(t), "eval", "scripts", "fix_retry.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Engine is a supervisor wired to the fake provider and either the Docker
// runner or the host interpreter.
type Engine struct {
	Store   *store.Store
	Orch    *orch.Orchestrator
	Runner  *sandbox.Runner
	DataDir string

	mu      sync.Mutex
	created int
	runIDs  []string
	dirs    []*workspace.Dir
	images  profile.Images

	// Provider, when set, answers model turns. ProviderName is stored on the run.
	// ModelName is the configured identity recorded for the fake provider.
	Provider     model.Provider
	ProviderName string
	ModelName    string
}

func NewEngine(t *testing.T, st *store.Store, hooks orch.Hooks) *Engine {
	t.Helper()
	images := TestImages(t)
	eng := &Engine{Store: st, DataDir: t.TempDir(), images: images}
	if len(images.List()) > 0 {
		eng.Runner = &sandbox.Runner{Images: images}
	}
	eng.Orch = orch.New(orch.Options{
		Store:   st,
		DataDir: eng.DataDir,
		Prices: usage.Prices{
			InputPerMillion:  100_000,
			OutputPerMillion: 500_000,
		},
		Hooks: hooks,
		ModelFor: func(current store.Run) (model.Provider, error) {
			if eng.Provider != nil {
				return eng.Provider, nil
			}
			script, err := model.ParseScript(current.ScriptJSON)
			if err != nil {
				return nil, err
			}
			fake := model.NewFake(script)
			fake.ModelName = eng.ModelName
			return fake, nil
		},
		NewWorkspace: func(current store.Run) (workspace.Handle, error) {
			eng.mu.Lock()
			eng.created++
			eng.runIDs = append(eng.runIDs, current.ID)
			eng.mu.Unlock()
			prof, err := profile.Parse(current.ProfileJSON)
			if err != nil {
				return nil, err
			}
			dir := workspace.NewDir(current.Worktree, orch.BasePath(current.Worktree), func(ctx context.Context, root string) (string, string, int, error) {
				if eng.Runner != nil {
					return eng.Runner.RunProfile(ctx, root, current.ID, prof)
				}
				return workspace.HostExec(ctx, root, prof)
			})
			if eng.Runner != nil {
				dir.CloseFn = func(ctx context.Context) error {
					return eng.Runner.Destroy(ctx, current.ID)
				}
			}
			eng.mu.Lock()
			eng.dirs = append(eng.dirs, dir)
			eng.mu.Unlock()
			return dir, nil
		},
	})
	t.Cleanup(func() {
		if eng.Runner == nil {
			return
		}
		eng.mu.Lock()
		ids := append([]string(nil), eng.runIDs...)
		eng.mu.Unlock()
		for _, runID := range ids {
			_ = eng.Runner.Destroy(context.Background(), runID)
		}
	})
	return eng
}

// PayrepoRequest is the explicit Python command for the retry fixture.
// It lives in the test helper. Production does not assume this argv.
func PayrepoRequest() profile.Request {
	return profile.Request{
		Name: profile.NamePython,
		Argv: []string{"python", "-m", "unittest", "tests.test_retry"},
	}
}

func (e *Engine) Create(t *testing.T, goal, repo string, budget, maxSteps int, script []byte) store.Run {
	t.Helper()
	return e.CreateWithProfile(t, goal, repo, budget, maxSteps, script, e.MustProfile(t, PayrepoRequest()))
}

// CreateInteraction stores ask, plan, or agent on the run. The daemon policy
// reads that field. A terminal session is not required.
func (e *Engine) CreateInteraction(t *testing.T, goal, repo, interaction string, budget, maxSteps int, script []byte) store.Run {
	t.Helper()
	prof := e.MustProfile(t, PayrepoRequest())
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := e.Store.CreateRun(context.Background(), store.CreateInput{
		Goal:         goal,
		RepoPath:     repo,
		BudgetCents:  budget,
		MaxSteps:     maxSteps,
		WallSeconds:  300,
		AgentVersion: version.Compute(e.images.Python, e.images.Go, e.images.Node),
		Provider:     providerName(e),
		RequestHash:  id.New("req_"),
		ScriptJSON:   script,
		ProfileJSON:  raw,
		Interaction:  interaction,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return created
}

func (e *Engine) CreateWithProfile(t *testing.T, goal, repo string, budget, maxSteps int, script []byte, prof profile.Profile) store.Run {
	t.Helper()
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := e.Store.CreateRun(context.Background(), store.CreateInput{
		Goal:         goal,
		RepoPath:     repo,
		BudgetCents:  budget,
		MaxSteps:     maxSteps,
		WallSeconds:  300,
		AgentVersion: version.Compute(e.images.Python, e.images.Go, e.images.Node),
		Provider:     providerName(e),
		RequestHash:  id.New("req_"),
		ScriptJSON:   script,
		ProfileJSON:  raw,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return created
}

func (e *Engine) MustProfile(t *testing.T, req profile.Request) profile.Profile {
	t.Helper()
	images := e.images
	if len(images.List()) == 0 {
		images = profile.Images{Python: "host", Go: "host", Node: "host"}
	}
	prof, err := profile.Resolve(req, images, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	return prof
}

// TestImages reads the sandbox tags. When any tag is set, all three must be
// set and present. An unset group uses the host runner and is not container support.
func TestImages(t *testing.T) profile.Images {
	t.Helper()
	python := os.Getenv("AGT_IMAGE_PYTHON")
	if python == "" {
		python = os.Getenv("AGT_SANDBOX_IMAGE")
	}
	goImage := os.Getenv("AGT_IMAGE_GO")
	node := os.Getenv("AGT_IMAGE_NODE")
	if python == "" && goImage == "" && node == "" {
		if os.Getenv("AGT_INTEGRATION") == "1" {
			t.Fatal("AGT_INTEGRATION=1 requires AGT_IMAGE_PYTHON, AGT_IMAGE_GO, and AGT_IMAGE_NODE")
		}
		return profile.Images{}
	}
	if python == "" || goImage == "" || node == "" {
		t.Fatal("set AGT_IMAGE_PYTHON or AGT_SANDBOX_IMAGE, AGT_IMAGE_GO, and AGT_IMAGE_NODE together")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("sandbox images are set and docker is absent: %v", err)
	}
	for _, image := range []string{python, goImage, node} {
		if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
			t.Fatalf("sandbox image %s is not available: %v", image, err)
		}
	}
	return profile.Images{Python: python, Go: goImage, Node: node}
}

// missingPrereq reports whether a missing dependency must fail the test.
// An integration run, or a dependency the caller explicitly configured, fails
// closed. An ordinary unit run may skip. A skip is not evidence.
func missingPrereq(integration, explicit bool) bool {
	return integration || explicit
}

// MissingPrereq reads AGT_INTEGRATION. The value "1" is an integration run.
func MissingPrereq(explicit bool) bool {
	return missingPrereq(os.Getenv("AGT_INTEGRATION") == "1", explicit)
}

func providerName(e *Engine) string {
	if e.ProviderName != "" {
		return e.ProviderName
	}
	return "fake"
}

func (e *Engine) Created() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.created
}

func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, dir := range e.dirs {
		out = append(out, dir.Calls()...)
	}
	return out
}

func WaitState(t *testing.T, st *store.Store, runID string, want run.State, d time.Duration) store.Run {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		current, err := st.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == want {
			return current
		}
		if run.IsTerminal(current.State) {
			Dump(t, st, runID)
			t.Fatalf("state %s, want %s", current.State, want)
		}
		if time.Now().After(deadline) {
			Dump(t, st, runID)
			t.Fatalf("timed out in %s, want %s", current.State, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func Dump(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	current, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Logf("get run: %v", err)
		return
	}
	t.Logf("state=%s version=%d err=%q steps=%d spent=%d worktree=%s",
		current.State, current.StateVersion, current.Error, current.StepCount, current.SpentMicros, current.Worktree)
	events, err := st.EventsAfter(ctx, runID, 0)
	if err != nil {
		t.Logf("events: %v", err)
	}
	for _, ev := range events {
		t.Logf("  #%d %s %s", ev.Seq, ev.Type, ev.Payload)
	}
	cmds, err := st.ListCommands(ctx, runID)
	if err != nil {
		t.Logf("commands: %v", err)
	}
	for _, cmd := range cmds {
		t.Logf("  cmd %s %s %s result=%s", cmd.CommandID, cmd.Tool, cmd.State, cmd.Result)
	}
}
