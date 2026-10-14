package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRunLifecyclePostsTheRunAndLeavesAttachAsDetach(t *testing.T) {
	var posts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			writeTestHealth(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token-value1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		posts = append(posts, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runs/run_1/pause":
			_, _ = w.Write([]byte(`{"id":"run_1","state":"PAUSED"}`))
		case "/v1/runs/run_1/resume":
			_, _ = w.Write([]byte(`{"id":"run_1","state":"PLANNING"}`))
		case "/v1/runs/run_1/budget-resume":
			_, _ = w.Write([]byte(`{"id":"run_1","state":"PLANNING","budget_cents":80}`))
		case "/v1/runs/run_1/cancel":
			_, _ = w.Write([]byte(`{"id":"run_1","state":"CANCELLED"}`))
		case "/v1/cleanup":
			_, _ = w.Write([]byte(`{"removed":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")
	t.Setenv("AGT_SERVER", ts.URL)

	if code := Run([]string{"agt", "run", "pause", "run_1", "--server", ts.URL}); code != 0 {
		t.Fatalf("pause %d", code)
	}
	if code := Run([]string{"agt", "run", "budget-resume", "run_1", "--budget-cents", "80", "--server", ts.URL}); code != 0 {
		t.Fatalf("budget-resume %d", code)
	}
	if code := Run([]string{"agt", "run", "cancel", "run_1", "--server", ts.URL}); code != 0 {
		t.Fatalf("cancel %d", code)
	}
	if code := Run([]string{"agt", "run", "cleanup", "--server", ts.URL}); code != 0 {
		t.Fatalf("cleanup %d", code)
	}
	got := strings.Join(posts, "\n")
	for _, want := range []string{
		`POST /v1/runs/run_1/pause {}`,
		`POST /v1/runs/run_1/budget-resume {"budget_cents":80}`,
		`POST /v1/runs/run_1/cancel {}`,
		`POST /v1/cleanup {}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "/attach") {
		t.Fatalf("attach posted %s", got)
	}
	var stderr strings.Builder
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code := Run([]string{"agt", "help"})
	w.Close()
	os.Stderr = old
	_, _ = io.Copy(&stderr, r)
	if code != 0 || !strings.Contains(stderr.String(), "does not cancel") {
		t.Fatalf("help %d %s", code, stderr.String())
	}
}
