package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	current = startAgtd(t, agtdBin, data, addr)
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
	current = startAgtd(t, agtdBin, data, addr)

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func startAgtd(t *testing.T, bin, data, addr string) *exec.Cmd {
	t.Helper()
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
		cmd.Env = append(os.Environ(),
			"AGT_LISTEN="+addr,
			"AGT_DATA_DIR="+data,
			"AGT_DATABASE_URL="+testutil.DatabaseURL(),
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
