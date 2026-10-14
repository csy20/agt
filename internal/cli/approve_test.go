package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApproveAcceptsFlagsAfterIDs(t *testing.T) {
	var got struct {
		path string
		body map[string]any
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_1/approvals":
			got.path = r.URL.Path
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
				t.Errorf("decode: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"state":"EXECUTING"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	code := Run([]string{"agt", "approve", "--server", ts.URL, "run_1", "cmd_1", "--args-hash", "abc123", "--decision", "approved"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.path != "/v1/runs/run_1/approvals" {
		t.Fatalf("path %q", got.path)
	}
	if got.body["command_id"] != "cmd_1" || got.body["args_hash"] != "abc123" || got.body["decision"] != "approved" {
		t.Fatalf("body %#v", got.body)
	}
}

func TestFixSendsAbsolutePathsFromAnotherDirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "demo.json")
	if err := os.WriteFile(script, []byte(`{"steps":[{"finish":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	clientDir := t.TempDir()
	daemonDir := t.TempDir()
	repoRel, err := filepath.Rel(clientDir, repo)
	if err != nil {
		t.Fatal(err)
	}
	scriptRel, err := filepath.Rel(clientDir, script)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		repo   string
		script string
		prov   string
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs" {
			var body struct {
				Repo     string `json:"repo_path"`
				Script   string `json:"script_path"`
				Provider string `json:"provider"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			got.repo = body.Repo
			got.script = body.Script
			got.prov = body.Provider
			resolved := body.Repo
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(daemonDir, resolved)
			}
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("daemon cwd %s cannot see repo path %q: %v", daemonDir, body.Repo, err)
			}
			_, _ = w.Write([]byte(`{"run_id":"run_abs","state":"CREATED"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			return
		}
		if r.URL.Path == "/v1/runs/run_abs" {
			_, _ = io.WriteString(w, `{"state":"FAILED_TERMINAL","error":"stopped in test"}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)

	chdir(t, clientDir)
	code := Run([]string{
		"agt", "fix", "--server", ts.URL, "--provider", "fake", "--script", scriptRel,
		"--repo", repoRel, "make the retry test pass",
	})
	if code == 2 {
		t.Fatal("fix rejected the documented arguments")
	}
	wantRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	wantScript, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	if got.repo != wantRepo {
		t.Fatalf("repo_path %q want %q", got.repo, wantRepo)
	}
	if got.script != wantScript {
		t.Fatalf("script_path %q want %q", got.script, wantScript)
	}
	if !filepath.IsAbs(got.repo) || !filepath.IsAbs(got.script) {
		t.Fatalf("paths were not absolute: %#v", got)
	}
	if got.prov != "fake" {
		t.Fatalf("provider %q", got.prov)
	}
}

func TestFixDoesNotSubstituteSampleScript(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "eval", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(root, "eval", "scripts", "fix_retry.json")
	if err := os.WriteFile(sample, []byte(`{"steps":[{"finish":true,"text":"known solution"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	posts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		posts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	t.Setenv("AGT_PROVIDER", "")
	chdir(t, root)
	code := Run([]string{"agt", "fix", "--server", ts.URL, "--provider", "fake", "--repo", repo, "fix an unrelated bug"})
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if posts != 0 {
		t.Fatalf("sample script was sent to the daemon (%d posts)", posts)
	}
}

func TestProviderFlagRespectsEnvironment(t *testing.T) {
	repo := t.TempDir()
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs" {
			var body struct {
				Provider string `json:"provider"`
				Script   string `json:"script_path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = body.Provider
			if body.Script != "" {
				t.Errorf("openai run included script %s", body.Script)
			}
			_, _ = w.Write([]byte(`{"run_id":"run_p","state":"CREATED"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			return
		}
		_, _ = io.WriteString(w, `{"state":"FAILED_TERMINAL","error":"stopped in test"}`)
	}))
	t.Cleanup(ts.Close)

	t.Setenv("AGT_PROVIDER", "openai")
	code := Run([]string{"agt", "fix", "--server", ts.URL, "--repo", repo, "repair the test"})
	if code == 2 {
		t.Fatal("AGT_PROVIDER=openai was rejected")
	}
	if got != "openai" {
		t.Fatalf("provider sent %q", got)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}
