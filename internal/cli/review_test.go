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

func TestRunReviewShowsTheArtifactBeforeApply(t *testing.T) {
	var posted string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			writeTestHealth(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token-value1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_1/diff":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"diff":"--- a/a.txt\n+++ b/a.txt\n","digest":"abc123","complete":true,"repo_path":"/repo"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_1/patch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"digest":"abc123","repo_path":"/repo","files":[{"op":"modify","path":"a.txt","before_mode":"0644","after_mode":"0644","before_sha256":"beforehashvalue","after_sha256":"afterhashvalue"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_1/apply":
			body, _ := io.ReadAll(r.Body)
			posted = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"applied":1,"digest":"abc123"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")

	code := Run([]string{"agt", "run", "apply", "run_1", "--server", ts.URL})
	if code != 2 {
		t.Fatalf("apply without digest exited %d", code)
	}
	if posted != "" {
		t.Fatalf("apply without digest posted %s", posted)
	}

	code = Run([]string{"agt", "run", "apply", "--server", ts.URL, "run_1", "--digest", "nope"})
	if code != 2 {
		t.Fatalf("mismatched digest exited %d", code)
	}
	if posted != "" {
		t.Fatal("mismatched digest was posted")
	}

	code = Run([]string{"agt", "run", "apply", "run_1", "--digest", "abc123", "--server", ts.URL})
	if code != 0 {
		t.Fatalf("apply exited %d", code)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(posted), &body); err != nil {
		t.Fatal(err)
	}
	if body["digest"] != "abc123" || body["repo_path"] != nil {
		t.Fatalf("posted %#v", body)
	}

	out := filepath.Join(t.TempDir(), "patch.json")
	code = Run([]string{"agt", "run", "export", "run_1", "--output", out, "--server", ts.URL})
	if code != 0 {
		t.Fatalf("export exited %d", code)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"digest":"abc123"`) {
		t.Fatalf("export %s", raw)
	}
}
