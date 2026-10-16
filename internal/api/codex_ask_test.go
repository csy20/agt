package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/testutil"
)

func TestCodexAskRefusesWithoutAuthorization(t *testing.T) {
	srv := api.New(nil, config.Config{}, testToken)
	srv.CodexCommand = []string{"/bin/false"}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	body := bytes.NewBufferString(`{"repo_path":"/tmp","prompt":"explain","authorize":false}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/agents/codex/ask", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || out["turn_started"] != false || out["prompt_sent"] != false || out["live"] != false {
		t.Fatalf("status %d body %+v", resp.StatusCode, out)
	}
}

func TestCodexAskStopsBeforeThePromptWhenTheSandboxCanReadOutside(t *testing.T) {
	st := testutil.Open(t)
	repo := t.TempDir()
	original := []byte("def charge(amount):\n    return amount + amount\n")
	if err := os.WriteFile(filepath.Join(repo, "processor.py"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "stub.log")
	script := filepath.Join(t.TempDir(), "stub.py")
	if err := os.WriteFile(script, []byte(codexAskStub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGT_STUB_LOG", logPath)
	srv := api.New(st, config.Config{DataDir: data, Provider: "fake"}, testToken)
	srv.CodexCommand = []string{"python3", script}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	payload, _ := json.Marshal(map[string]any{
		"repo_path": repo,
		"prompt":    "explain-charge-marker",
		"authorize": true,
	})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/agents/codex/ask", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Error       string `json:"error"`
		Label       string `json:"label"`
		Live        bool   `json:"live"`
		TurnStarted bool   `json:"turn_started"`
		PromptSent  bool   `json:"prompt_sent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusConflict || out.TurnStarted || out.PromptSent || out.Live || out.Label != "blocked" {
		t.Fatalf("status %d %+v", resp.StatusCode, out)
	}
	if !strings.Contains(out.Error, "outside the snapshot") || strings.Contains(out.Error, "explain-charge-marker") {
		t.Fatal(out.Error)
	}
	got, err := os.ReadFile(filepath.Join(repo, "processor.py"))
	if err != nil || string(got) != string(original) {
		t.Fatalf("source changed %q %v", got, err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "explain-charge-marker") || strings.Contains(string(raw), "turn/start") {
		t.Fatalf("prompt sent:\n%s", raw)
	}
	runs, err := st.ListRuns(context.Background(), 20)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs %d %v", len(runs), err)
	}
	sessions, err := st.ListSessions(context.Background(), repo, 20)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions %d %v", len(sessions), err)
	}
}

const codexAskStub = `import json, sys, os
if "--version" in sys.argv:
    print("stub-codex 0")
    raise SystemExit(0)
if "login" in sys.argv:
    print("Logged in using ChatGPT")
    raise SystemExit(0)
log = os.environ.get("AGT_STUB_LOG", "")
if log:
    with open(log, "a", encoding="utf-8") as fh:
        fh.write("argv " + " ".join(sys.argv) + "\n")
for line in sys.stdin:
    if log:
        with open(log, "a", encoding="utf-8") as fh:
            fh.write(line)
    msg = json.loads(line)
    method = msg.get("method")
    if "id" not in msg:
        continue
    if method == "initialize":
        body = {"userAgent": "stub-codex"}
    elif method == "config/read":
        body = {
            "config": {
                "sandbox_mode": None,
                "default_permissions": "agt-snapshot",
                "permissions": {"agt-snapshot": {}},
                "features": {"shell_tool": False},
                "mcp_servers": {"node_repl": {"enabled": False}},
            },
            "origins": {},
        }
    elif method == "command/exec":
        argv = msg.get("params", {}).get("command") or [""]
        if argv[0] == "cat":
            body = {"exitCode": 0, "stdout": "agt-boundary-canary\n", "stderr": ""}
        else:
            body = {"exitCode": 1, "stdout": "", "stderr": ""}
    else:
        continue
    sys.stdout.write(json.dumps({"id": msg["id"], "result": body}) + "\n")
    sys.stdout.flush()
`
