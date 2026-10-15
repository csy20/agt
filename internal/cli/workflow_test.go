package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/version"
)

func writeTestHealth(w http.ResponseWriter) {
	w.Header().Set(version.ProductHeader, version.Name)
	w.Header().Set(version.VersionHeader, version.Product)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func captureStdout(t *testing.T, fn func()) (out string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(r)
		done <- string(body)
	}()
	finished := false
	defer func() {
		os.Stdout = old
		_ = w.Close()
		if !finished {
			<-done
		}
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	finished = true
	return <-done
}

func TestDiscoverRejectsAnyHTTP200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	}))
	t.Cleanup(ts.Close)
	err := discover(ts.URL)
	if err == nil || strings.Contains(err.Error(), "agtd is not running") {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), "Next:") || !strings.Contains(err.Error(), version.ProductHeader) {
		t.Fatalf("error %v", err)
	}
}

func TestDiscoverAcceptsThisVersionOnly(t *testing.T) {
	var versionHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if versionHeader == "" {
			writeTestHealth(w)
			return
		}
		w.Header().Set(version.ProductHeader, version.Name)
		w.Header().Set(version.VersionHeader, versionHeader)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	if err := discover(ts.URL); err != nil {
		t.Fatal(err)
	}
	versionHeader = "9.9.9"
	err := discover(ts.URL)
	if err == nil || !strings.Contains(err.Error(), "9.9.9") || !strings.Contains(err.Error(), "Next:") {
		t.Fatalf("error %v", err)
	}
}

func TestReadSSEKeepsTheCursorAndSkipsHeartbeats(t *testing.T) {
	in := ": heartbeat\n\nid: 4\nevent: command.finished\ndata: {\"tool\":\"run_test\",\"exit_code\":1,\"output_excerpt\":\"FAIL retry\"}\n\n"
	var seq int64
	var event string
	var data string
	err := readSSE(strings.NewReader(in), func(got int64, name string, body []byte) {
		seq = got
		event = name
		data = string(body)
	})
	if err != io.EOF {
		t.Fatal(err)
	}
	if seq != 4 || event != "command.finished" || !strings.Contains(data, "run_test") {
		t.Fatalf("seq %d event %s data %s", seq, event, data)
	}
	got := formatEvent(event, []byte(data))
	if !strings.Contains(got, "tool run_test exit 1") || !strings.Contains(got, "FAIL retry") {
		t.Fatalf("line %q", got)
	}
	if formatEvent("verification.failed", []byte(`{"exit_code":1,"outcome":"repeated_verification"}`)) != "verification failed exit 1 outcome repeated_verification" {
		t.Fatal(formatEvent("verification.failed", []byte(`{"exit_code":1,"outcome":"repeated_verification"}`)))
	}
	if formatEvent("baseline.recorded", []byte(`{"exit_code":1}`)) != "baseline exit 1" {
		t.Fatal("baseline")
	}
}

func TestAttachReconnectsWithoutReplaying(t *testing.T) {
	var events, gets int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			writeTestHealth(w)
		case strings.Contains(r.URL.Path, "/events"):
			events++
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if events == 1 {
				if r.URL.Query().Get("after") != "0" {
					t.Errorf("first after %s", r.URL.Query().Get("after"))
				}
				_, _ = io.WriteString(w, "id: 2\nevent: state.changed\ndata: {\"from\":\"CREATED\",\"to\":\"PLANNING\"}\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				hj, ok := w.(http.Hijacker)
				if !ok {
					return
				}
				conn, bufrw, err := hj.Hijack()
				if err != nil {
					return
				}
				_ = bufrw.Flush()
				_ = conn.Close()
				return
			}
			if r.URL.Query().Get("after") != "2" {
				t.Errorf("second after %s", r.URL.Query().Get("after"))
			}
			_, _ = io.WriteString(w, "id: 3\nevent: run.succeeded\ndata: {}\n\n")
		case r.URL.Path == "/v1/runs/run_1/diff":
			_, _ = io.WriteString(w, `{"digest":"abc123","complete":true}`)
		case r.URL.Path == "/v1/runs/run_1":
			gets++
			if gets == 1 {
				_, _ = io.WriteString(w, `{"state":"EXECUTING","spent_micros":0,"budget_cents":100}`)
				return
			}
			_, _ = io.WriteString(w, `{"state":"SUCCEEDED","spent_micros":15,"budget_cents":100,"repo_path":"/repo","worktree":"/work"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")
	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "run", "attach", "run_1", "--server", ts.URL})
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Count(out, "PLANNING") != 1 {
		t.Fatalf("replayed or missed planning:\n%s", out)
	}
	if strings.Count(out, "succeeded") != 1 {
		t.Fatalf("succeeded count\n%s", out)
	}
	if !strings.Contains(out, "agt run apply run_1 --digest abc123") {
		t.Fatalf("missing apply hint\n%s", out)
	}
	if events < 2 {
		t.Fatalf("events %d", events)
	}
}

func TestModelFlagRejectsADifferentDaemonModel(t *testing.T) {
	posts := 0
	repo := t.TempDir()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			writeTestHealth(w)
		case r.URL.Path == "/v1/identity":
			fmt.Fprintf(w, `{"product":%q,"version":%q,"provider":"openai","model":"other"}`, version.Name, version.Product)
		case r.URL.Path == "/v1/runs":
			posts++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")
	code := Run([]string{"agt", "fix", "--server", ts.URL, "--model", "wanted", "--repo", repo, "--profile", "python", "--test-arg", "python", "repair the test"})
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if posts != 0 {
		t.Fatalf("posted %d", posts)
	}
}

func TestRunListPrintsReviewCommands(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			writeTestHealth(w)
		case r.URL.Path == "/v1/runs":
			if r.Header.Get("Authorization") != "Bearer test-token-value1" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"runs":[{"id":"run_1","state":"SUCCEEDED","goal":"fix retry","spent_micros":10,"repo_path":"/repo"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")
	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "run", "list", "--server", ts.URL, "--json"})
	})
	if code != 0 || !strings.Contains(out, "run_1") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	out = captureStdout(t, func() {
		code = Run([]string{"agt", "run", "list", "--server", ts.URL})
	})
	if code != 0 || !strings.Contains(out, "agt run diff RUN") || !strings.Contains(out, "agt run apply RUN --digest DIGEST") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestDoctorNamesTheFirstFailure(t *testing.T) {
	report := assembleDoctor([]doctorCheck{
		{Name: "docker", OK: false, Detail: "docker was not found on PATH", Next: "Install Docker and start its daemon. Then run `agt doctor`"},
		{Name: "postgres", OK: false, Detail: "down", Next: "start postgres"},
	})
	if report.OK || !strings.Contains(report.Next, "Install Docker") {
		t.Fatalf("%+v", report)
	}
	text := formatDoctor(report)
	if !strings.Contains(text, "fail  docker") || !strings.Contains(text, "next  Install Docker") {
		t.Fatalf("text\n%s", text)
	}
	if probeListen("0.0.0.0:8787").OK || !strings.Contains(probeListen("0.0.0.0:8787").Next, "AGT_LISTEN") {
		t.Fatal(probeListen("0.0.0.0:8787"))
	}
	if !probeListen("127.0.0.1:8787").OK {
		t.Fatal("loopback")
	}
}

func TestLooseTokenAndDataDirNameTheNextCommand(t *testing.T) {
	t.Setenv("AGT_TOKEN", "")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := probeDataDir(dir)
	if data.OK || !strings.Contains(data.Next, "chmod 700") {
		t.Fatalf("%+v", data)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.token")
	if err := os.WriteFile(path, []byte("test-token-value1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	token := probeToken(dir)
	if token.OK || !strings.Contains(token.Next, "chmod 600") {
		t.Fatalf("%+v", token)
	}
}

func TestDockerCheckNamesInstallWhenTheBinaryIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	checks := probeDocker([]string{"agt-sandbox:python"})
	if len(checks) != 2 || checks[0].OK || !strings.Contains(checks[0].Next, "Install Docker") {
		t.Fatalf("%+v", checks)
	}
	if checks[1].Name != "images" || !strings.Contains(checks[1].Next, "make image") {
		t.Fatalf("%+v", checks[1])
	}
}

func TestCompletionCoversTheShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var code int
		out := captureStdout(t, func() {
			code = Run([]string{"agt", "completion", shell})
		})
		if code != 0 || !strings.Contains(out, "doctor") || !strings.Contains(out, "budget-resume") {
			t.Fatalf("%s exit %d\n%s", shell, code, out)
		}
	}
	if code := Run([]string{"agt", "completion", "tcsh"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestColorStaysOffForPipesAndNO_COLOR(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	t.Setenv("NO_COLOR", "")
	if colorEnabled() {
		t.Fatal("a pipe should stay plain")
	}
	t.Setenv("NO_COLOR", "1")
	os.Stdout = old
	if colorEnabled() {
		t.Fatal("NO_COLOR")
	}
}

func TestStartLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireStartLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		second, err := acquireStartLock(dir)
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(got)
			return
		}
		close(got)
		second()
	}()
	select {
	case <-got:
		release()
		t.Fatal("second start acquired the lock while the first held it")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second start did not acquire the lock")
	}
}

func TestRecordedDaemonURLRejectsAPublicHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGT_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "agtd.addr"), []byte("http://192.168.1.20:8787\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recordedDaemonURL() != "" {
		t.Fatal(recordedDaemonURL())
	}
	if err := os.WriteFile(filepath.Join(dir, "agtd.addr"), []byte("http://127.0.0.1:8799\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recordedDaemonURL() != "http://127.0.0.1:8799" {
		t.Fatal(recordedDaemonURL())
	}
}
