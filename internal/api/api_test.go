package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

const testToken = "test-token-value1"

func TestHealthIdempotencyAndOpenAIGuard(t *testing.T) {
	st := testutil.Open(t)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake"}, testToken).Handler())
	t.Cleanup(ts.Close)

	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health %d", health.StatusCode)
	}

	body := map[string]any{
		"goal":         "same goal",
		"repo_path":    testutil.Fixture(t),
		"provider":     "fake",
		"script_path":  filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"budget_cents": 100,
		"max_steps":    0,
		"profile":      pythonProfile(),
	}
	first := postRun(t, ts.URL, body, "idem-1")
	if first.status != http.StatusAccepted {
		t.Fatalf("create %d %s", first.status, first.body)
	}
	second := postRun(t, ts.URL, body, "idem-1")
	if second.status != http.StatusOK || second.runID != first.runID {
		t.Fatalf("replay %d %s want %s", second.status, second.runID, first.runID)
	}
	body["goal"] = "different goal"
	third := postRun(t, ts.URL, body, "idem-1")
	if third.status != http.StatusConflict {
		t.Fatalf("conflict %d %s", third.status, third.body)
	}

	openai := postRun(t, ts.URL, map[string]any{
		"goal":      "needs a key",
		"repo_path": testutil.Fixture(t),
		"provider":  "openai",
	}, "idem-openai")
	if openai.status != http.StatusBadRequest || !strings.Contains(openai.body, "AGT_MODEL_API_KEY") {
		t.Fatalf("openai guard %d %s", openai.status, openai.body)
	}

	view := getJSON(t, ts.URL+"/v1/runs/"+first.runID)
	if _, ok := view["script_json"]; ok {
		t.Fatal("run view included script_json")
	}
	if view["usage_micros"] == nil {
		t.Fatal("run view missing usage_micros")
	}
	prof, _ := view["profile"].(map[string]any)
	argv, _ := prof["argv"].([]any)
	image, _ := prof["image"].(string)
	if prof["name"] != "python" || image == "" || len(argv) != 4 {
		t.Fatalf("profile %#v", view["profile"])
	}

	missing := postRun(t, ts.URL, map[string]any{
		"goal": "no profile", "repo_path": testutil.Fixture(t), "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
	}, "idem-noprofile")
	if missing.status != http.StatusBadRequest || !strings.Contains(missing.body, "profile") {
		t.Fatalf("missing profile %d %s", missing.status, missing.body)
	}
	imaged := postRun(t, ts.URL, map[string]any{
		"goal": "image", "repo_path": testutil.Fixture(t), "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"profile":     map[string]any{"name": "python", "argv": []string{"python"}, "image": "evil:latest"},
	}, "idem-image")
	if imaged.status != http.StatusBadRequest || !strings.Contains(imaged.body, "image") {
		t.Fatalf("image field %d %s", imaged.status, imaged.body)
	}
	goRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(goRepo, "go.mod"), []byte("module example.com/x\n\ngo 1.23.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept := postRun(t, ts.URL, map[string]any{
		"goal": "stay on python", "repo_path": goRepo, "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"profile":     pythonProfile(),
	}, "idem-keep-python")
	if kept.status != http.StatusAccepted {
		t.Fatalf("explicit profile %d %s", kept.status, kept.body)
	}
	keptView := getJSON(t, ts.URL+"/v1/runs/"+kept.runID)
	keptProfile, _ := keptView["profile"].(map[string]any)
	if keptProfile["name"] != "python" {
		t.Fatalf("repo marker overrode the profile: %#v", keptView["profile"])
	}
}

func pythonProfile() map[string]any {
	return map[string]any{
		"name": "python",
		"argv": []string{"python", "-m", "unittest", "tests.test_retry"},
	}
}

func TestScriptedPushIsDeniedNotSuccessful(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go (&orch.Worker{Store: st, Run: eng.Orch.Run}).Loop(ctx)

	script := []byte(`{"steps":[{"tool":"git_push","args":{"remote":"origin","branch":"agent/demo","commit":"abc123"},"input_tokens":20,"output_tokens":10}]}`)
	created := eng.Create(t, "push the branch", testutil.Fixture(t), 100, 16, script)
	end := testutil.WaitState(t, st, created.ID, run.FailedTerminal, 15*time.Second)
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].State != "denied" || cmds[0].Tool != "git_push" {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("commands %+v", cmds)
	}
	if !strings.Contains(string(cmds[0].Result), "unsupported") {
		t.Fatalf("result %s", cmds[0].Result)
	}
	if cmds[0].State == "succeeded" {
		t.Fatal("unsupported push was recorded as succeeded")
	}
	if eng.Created() != 0 {
		t.Fatalf("workspace created %d", eng.Created())
	}
	if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
		t.Fatalf("sandbox execs %d", eng.Runner.ExecCount())
	}
	if strings.Contains(strings.ToLower(end.Error), "pushed") {
		t.Fatalf("run claims a push: %s", end.Error)
	}
	assertNoContainer(t, created.ID)
}

func TestApprovalHashIsBoundAndPushStaysUnsupported(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	script := []byte(`{"steps":[{"finish":true,"text":"unused"}]}`)
	created := eng.Create(t, "hold for approval", testutil.Fixture(t), 100, 16, script)
	ctx := context.Background()
	cur, err := st.Transition(ctx, created.ID, created.StateVersion, run.Contextualising, "test")
	if err != nil {
		t.Fatal(err)
	}
	cur, err = st.Transition(ctx, cur.ID, cur.StateVersion, run.Planning, "test")
	if err != nil {
		t.Fatal(err)
	}
	cur, err = st.CommitModelIntent(ctx, cur.ID, cur.StateVersion, store.Intent{
		Kind:         "tool",
		Tool:         "git_push",
		Canonical:    []byte(`{"branch":"agent/demo","commit":"abc123","remote":"origin"}`),
		ArgsHash:     "hash-ok",
		Decision:     "require_approval",
		Reason:       "external mutation",
		CommandID:    "cmd_push",
		InputTokens:  20,
		OutputTokens: 10,
		CostMicros:   1,
		Model:        "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cur.State != run.AwaitingApproval {
		t.Fatalf("state %s", cur.State)
	}
	workCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go (&orch.Worker{Store: st, Run: eng.Orch.Run}).Loop(workCtx)

	before, err := st.MaxSeq(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := cur.StateVersion
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake"}, testToken).Handler())
	t.Cleanup(ts.Close)
	wrong := postJSON(t, ts.URL+"/v1/runs/"+created.ID+"/approvals", map[string]any{
		"command_id": "cmd_push",
		"args_hash":  "deadbeef",
		"decision":   "approved",
	}, "")
	wrong.body.Close()
	if wrong.status != http.StatusConflict {
		t.Fatalf("wrong hash %d", wrong.status)
	}
	after, err := st.MaxSeq(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("wrong hash wrote events %d -> %d", before, after)
	}
	current, err := st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.AwaitingApproval || current.StateVersion != version {
		t.Fatalf("state %s version %d", current.State, current.StateVersion)
	}
	if eng.Created() != 0 {
		t.Fatal("workspace created before a matching approval")
	}

	ok := postJSON(t, ts.URL+"/v1/runs/"+created.ID+"/approvals", map[string]any{
		"command_id": "cmd_push",
		"args_hash":  "hash-ok",
		"decision":   "approved",
	}, "")
	okBody, _ := io.ReadAll(ok.body)
	ok.body.Close()
	if ok.status != http.StatusOK {
		t.Fatalf("approve %d %s", ok.status, okBody)
	}
	end := testutil.WaitState(t, st, created.ID, run.FailedTerminal, 15*time.Second)
	finished, err := st.GetCommand(ctx, created.ID, "cmd_push")
	if err != nil {
		t.Fatal(err)
	}
	if finished.State == "succeeded" || !strings.Contains(string(finished.Result), "unsupported") {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("command %s %s", finished.State, finished.Result)
	}
	if eng.Created() != 0 {
		t.Fatalf("workspace created %d", eng.Created())
	}
	if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
		t.Fatalf("sandbox execs %d", eng.Runner.ExecCount())
	}
	if !strings.Contains(end.Error, "unsupported") {
		t.Fatalf("error %q", end.Error)
	}
	assertNoContainer(t, created.ID)
}

type posted struct {
	status int
	body   string
	runID  string
}

func postRun(t *testing.T, base string, body map[string]any, key string) posted {
	t.Helper()
	resp := postJSON(t, base+"/v1/runs", body, key)
	defer resp.body.Close()
	raw, _ := io.ReadAll(resp.body)
	var decoded struct {
		RunID string `json:"run_id"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &decoded)
	text := string(raw)
	if decoded.Error != "" {
		text = decoded.Error
	}
	return posted{status: resp.status, body: text, runID: decoded.RunID}
}

type response struct {
	status int
	body   io.ReadCloser
}

func postJSON(t *testing.T, url string, body map[string]any, key string) response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, body: resp.Body}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s %d %s", url, resp.StatusCode, raw)
	}
	var view map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func assertNoContainer(t *testing.T, runID string) {
	t.Helper()
	if os.Getenv("AGT_SANDBOX_IMAGE") == "" {
		return
	}
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=agt-run="+runID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container still present: %s", out)
	}
}
