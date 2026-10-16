package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/csy20/agt/internal/testutil"
)

func TestServerRestartKeepsTheEventLog(t *testing.T) {
	t.Setenv("AGT_TOKEN", testToken)
	root := testutil.RepoRoot(t)
	binDir := t.TempDir()
	agtdBin := buildBin(t, root, binDir, "agtd", "./cmd/agtd")
	agtBin := buildBin(t, root, binDir, "agt", "./cmd/agt")
	st := testutil.Open(t)

	addr := freeAddr(t)
	data := t.TempDir()
	var current *exec.Cmd
	t.Cleanup(func() {
		stopProcess(current)
	})
	current = startAgtd(t, agtdBin, data, addr, testutil.URL(st))
	base := "http://" + addr

	body := map[string]any{
		"goal":        "restart me",
		"repo_path":   testutil.Fixture(t),
		"provider":    "fake",
		"script_path": filepath.Join(root, "eval", "scripts", "fix_retry.json"),
		"max_steps":   0,
		"profile":     pythonProfile(),
	}
	created := postRun(t, base, body, "idem-restart")
	if created.status != http.StatusAccepted && created.status != http.StatusOK {
		t.Fatalf("create %d %s", created.status, created.body)
	}
	waitHTTPState(t, base, created.runID, "PAUSED_BUDGET")
	before := getBytes(t, base+"/v1/runs/"+created.runID+"/events")

	stopProcess(current)
	current = nil
	current = startAgtd(t, agtdBin, data, addr, testutil.URL(st))

	after := getBytes(t, base+"/v1/runs/"+created.runID+"/events")
	if !eventsEqual(t, before, after) {
		t.Fatalf("event log changed\nbefore %s\nafter %s", before, after)
	}
	view := getJSON(t, base+"/v1/runs/"+created.runID)
	if view["id"] != created.runID || view["goal"] != "restart me" || view["state"] != "PAUSED_BUDGET" {
		t.Fatalf("run after restart %#v", view)
	}
	if _, ok := view["script_json"]; ok {
		t.Fatal("run view included script_json")
	}

	cmd := exec.Command(agtBin, "run", "attach", created.runID)
	cmd.Env = append(os.Environ(), "AGT_URL="+base)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("attach exit %v\n%s", err, out)
	}
	text := string(out)
	var events []struct {
		Type    string          `json:"event_type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(before, &events); err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, ev := range events {
		if ev.Type != "state.changed" {
			continue
		}
		var payload struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("state %s -> %s", payload.From, payload.To)
		if !strings.Contains(text, line) {
			t.Fatalf("attach missing %q\n%s", line, text)
		}
		saw = true
	}
	if !saw || !strings.Contains(text, "PAUSED_BUDGET") {
		t.Fatalf("attach output\n%s", text)
	}
	_ = st
}

func TestModelSelectionSurvivesDaemonRestart(t *testing.T) {
	const secret = "sk-test-secret-value"
	root := testutil.RepoRoot(t)
	script := filepath.Join(root, "eval", "scripts", "fix_retry.json")
	t.Setenv("AGT_TOKEN", testToken)
	t.Setenv("AGT_PROVIDER", "fake")
	t.Setenv("AGT_MODEL_NAME", "scripted-model")
	t.Setenv("AGT_MODEL_API_KEY", secret)
	t.Setenv("AGT_MODELS", "local,cloud")
	t.Setenv("AGT_MODEL_LOCAL_ADAPTER", "fake")
	t.Setenv("AGT_MODEL_LOCAL_MODEL", "local-repair")
	t.Setenv("AGT_MODEL_LOCAL_SCRIPT", script)
	t.Setenv("AGT_MODEL_CLOUD_ADAPTER", "openai")
	t.Setenv("AGT_MODEL_CLOUD_MODEL", "cloud-model")
	t.Setenv("AGT_MODEL_CLOUD_ENDPOINT", "http://127.0.0.1:9/v1")

	binDir := t.TempDir()
	agtdBin := buildBin(t, root, binDir, "agtd", "./cmd/agtd")
	st := testutil.Open(t)
	addr := freeAddr(t)
	data := t.TempDir()
	var current *exec.Cmd
	t.Cleanup(func() { stopProcess(current) })
	current = startAgtd(t, agtdBin, data, addr, testutil.URL(st))
	base := "http://" + addr

	selected := postJSON(t, base+"/v1/models/select", map[string]any{"name": "local"}, "select-restart")
	selectedBody, _ := io.ReadAll(selected.body)
	selected.body.Close()
	if selected.status != http.StatusOK || strings.Contains(string(selectedBody), secret) {
		t.Fatalf("select %d leaked=%v", selected.status, strings.Contains(string(selectedBody), secret))
	}
	created := postRun(t, base, map[string]any{
		"goal":         "restart keeps " + secret,
		"repo_path":    testutil.Fixture(t),
		"model_config": "local",
		"max_steps":    0,
		"profile":      pythonProfile(),
	}, "idem-model-restart")
	if created.status != http.StatusAccepted && created.status != http.StatusOK {
		t.Fatalf("create %d %s", created.status, created.body)
	}
	waitHTTPState(t, base, created.runID, "PAUSED_BUDGET")
	before := getJSON(t, base+"/v1/runs/"+created.runID)
	if before["model_config"] != "local" || before["model_name"] != "local-repair" || before["scripted"] != true {
		t.Fatalf("before %#v", before)
	}
	events := getBytes(t, base+"/v1/runs/"+created.runID+"/events")
	if strings.Contains(string(events), secret) {
		t.Fatal("credential leaked in events")
	}

	stopProcess(current)
	current = nil
	current = startAgtd(t, agtdBin, data, addr, testutil.URL(st))
	listed := getJSON(t, base+"/v1/models")
	if listed["selected"] != "local" {
		t.Fatalf("selection after restart %#v", listed["selected"])
	}
	after := getJSON(t, base+"/v1/runs/"+created.runID)
	if after["model_config"] != "local" || after["model_name"] != "local-repair" || after["model_endpoint"] != before["model_endpoint"] {
		t.Fatalf("run changed %#v", after)
	}
	cloud := postJSON(t, base+"/v1/models/select", map[string]any{"name": "cloud"}, "select-cloud")
	cloudBody, _ := io.ReadAll(cloud.body)
	cloud.body.Close()
	if cloud.status != http.StatusOK || strings.Contains(string(cloudBody), secret) {
		t.Fatalf("cloud select %d", cloud.status)
	}
	second := postRun(t, base, map[string]any{
		"goal": "second", "repo_path": testutil.Fixture(t), "model_config": "cloud",
		"max_steps": 0, "profile": pythonProfile(),
	}, "idem-model-cloud")
	if second.status != http.StatusAccepted && second.status != http.StatusOK {
		t.Fatalf("second %d %s", second.status, second.body)
	}
	waitHTTPState(t, base, second.runID, "PAUSED_BUDGET")
	secondView := getJSON(t, base+"/v1/runs/"+second.runID)
	if secondView["model_config"] != "cloud" || secondView["model_name"] != "cloud-model" || secondView["model_endpoint"] != "http://127.0.0.1:9/v1" {
		t.Fatalf("second snapshot %#v", secondView)
	}
	still := getJSON(t, base+"/v1/runs/"+created.runID)
	if still["model_config"] != "local" || still["model_name"] != "local-repair" {
		t.Fatalf("first run moved %#v", still)
	}
	logBody, err := os.ReadFile(filepath.Join(data, "agtd.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logBody), secret) {
		t.Fatal("credential leaked in the daemon log")
	}
	_ = st
}

func buildBin(t *testing.T, root, dir, name, pkg string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	buf, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, buf)
	}
	return out
}

func freeAddr(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 8; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		if port == "8787" {
			continue
		}
		return addr
	}
	t.Fatal("no loopback port other than 8787")
	return ""
}

func startAgtd(t *testing.T, bin, data, addr, databaseURL string) *exec.Cmd {
	t.Helper()
	refuseSharedDatabase(t, databaseURL)
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "8787" {
		t.Fatalf("refusing listen address %s", addr)
	}
	logPath := filepath.Join(data, "agtd.log")
	var last []byte
	for attempt := 0; attempt < 3; attempt++ {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.Env = append(filterEnv(os.Environ(), "AGT_DATABASE_URL", "AGT_DATA_DIR", "AGT_LISTEN"),
			"AGT_LISTEN="+addr,
			"AGT_DATA_DIR="+data,
			"AGT_DATABASE_URL="+databaseURL,
			"AGT_PROVIDER=fake",
		)
		if err := cmd.Start(); err != nil {
			logFile.Close()
			t.Fatal(err)
		}
		if waitHealth(addr, 5*time.Second) {
			logFile.Close()
			return cmd
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logFile.Close()
		last, _ = os.ReadFile(logPath)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("agtd did not become healthy\n%s", last)
	return nil
}

func refuseSharedDatabase(t *testing.T, raw string) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Trim(parsed.Path, "/")
	if !strings.HasPrefix(name, "agt_test_") {
		t.Fatalf("agtd database %q is not a disposable test database", name)
	}
}

func filterEnv(env []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if drop[name] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func waitHealth(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	url := "http://" + addr + "/healthz"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func waitHTTPState(t *testing.T, base, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		view := getJSON(t, base+"/v1/runs/"+runID)
		if view["state"] == want {
			return
		}
		state, _ := view["state"].(string)
		if state == "SUCCEEDED" || state == "FAILED_TERMINAL" || state == "CANCELLED" {
			t.Fatalf("state %s, want %s", state, want)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", want)
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
}

func getBytes(t *testing.T, url string) []byte {
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s %d %s", url, resp.StatusCode, body)
	}
	return body
}

func eventsEqual(t *testing.T, before, after []byte) bool {
	t.Helper()
	type event struct {
		Seq     int64           `json:"sequence_no"`
		Type    string          `json:"event_type"`
		Payload json.RawMessage `json:"payload"`
	}
	var left, right []event
	if err := json.Unmarshal(before, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &right); err != nil {
		t.Fatal(err)
	}
	if len(left) != len(right) || len(left) == 0 {
		return false
	}
	for i := range left {
		if left[i].Seq != right[i].Seq || left[i].Type != right[i].Type || !bytes.Equal(left[i].Payload, right[i].Payload) {
			return false
		}
	}
	return true
}
