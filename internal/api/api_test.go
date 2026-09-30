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
	"github.com/csy20/agt/internal/testutil"
)

func TestHealthIdempotencyAndOpenAIGuard(t *testing.T) {
	st := testutil.Open(t)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake"}).Handler())
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
}

func TestApprovalBindsHashAndDoesNotExec(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go (&orch.Worker{Store: st, Run: eng.Orch.Run}).Loop(ctx)

	script := []byte(`{"steps":[{"tool":"git_push","args":{"remote":"origin","branch":"agent/demo","commit":"abc123"},"input_tokens":20,"output_tokens":10}]}`)
	created := eng.Create(t, "push the branch", testutil.Fixture(t), 100, 16, script)
	current := testutil.WaitState(t, st, created.ID, run.AwaitingApproval, 15*time.Second)
	time.Sleep(300 * time.Millisecond)
	before, err := st.MaxSeq(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := current.StateVersion
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].State != "awaiting_approval" {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("commands %+v", cmds)
	}
	cmd := cmds[0]

	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake"}).Handler())
	t.Cleanup(ts.Close)
	wrong := postJSON(t, ts.URL+"/v1/runs/"+created.ID+"/approvals", map[string]any{
		"command_id": cmd.CommandID,
		"args_hash":  "deadbeef",
		"decision":   "approved",
	}, "")
	wrong.body.Close()
	if wrong.status != http.StatusConflict {
		t.Fatalf("wrong hash %d", wrong.status)
	}
	after, err := st.MaxSeq(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("wrong hash wrote events %d -> %d", before, after)
	}
	current, err = st.GetRun(context.Background(), created.ID)
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
		"command_id": cmd.CommandID,
		"args_hash":  cmd.ArgsHash,
		"decision":   "approved",
	}, "")
	okBody, _ := io.ReadAll(ok.body)
	ok.body.Close()
	if ok.status != http.StatusOK {
		t.Fatalf("approve %d %s", ok.status, okBody)
	}
	end := testutil.WaitState(t, st, created.ID, run.FailedTerminal, 15*time.Second)
	finished, err := st.GetCommand(context.Background(), created.ID, cmd.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != "succeeded" || !strings.Contains(string(finished.Result), "no process was spawned") {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("command %s %s", finished.State, finished.Result)
	}
	if eng.Created() != 0 {
		t.Fatalf("workspace created %d", eng.Created())
	}
	if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
		t.Fatalf("sandbox execs %d", eng.Runner.ExecCount())
	}
	if !strings.Contains(end.Error, "no step") {
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
	resp, err := http.Get(url)
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
