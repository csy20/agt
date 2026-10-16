package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAgentAskDoesNotStartWithoutAuthorization(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	var code int
	errText := capture(t, &os.Stderr, func() {
		code = Run([]string{"agt", "agent", "ask", "--server", ts.URL, "--repo", t.TempDir(), "--prompt", "explain"})
	})
	if code != 2 || len(paths) != 0 || !strings.Contains(errText, "--authorize-live") {
		t.Fatalf("code %d paths %v err %s", code, paths, errText)
	}
}

func TestAgentListDoesNotStartAnAgent(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/runs" || strings.Contains(r.URL.Path, "chat") {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			writeTestHealth(w)
		case "/v1/agents":
			if r.Header.Get("Authorization") != "Bearer test-token-value1" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"entries":[{"kind":"model-api","name":"default","adapter":"fake","model":"local-repair","scripted":true,"valid":true,"execution":"scripted","live_inference":false},{"kind":"coding-agent","name":"codex","protocol":"codex-exec-jsonl/1","installed":true,"runtime_version":"codex-cli 0.162.1","runtime_supported":true,"authentication":"not-probed","execution":"unavailable","mutation":"unavailable","scripted":true,"live_inference":false,"reason":"fixtures only"}],"notice":"No inference request was made. No coding agent was started. Scripted protocol fixtures are not a live Codex or Claude run."}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")

	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "agent", "list", "--server", ts.URL})
	})
	if code != 0 || !strings.Contains(out, "model-api") || !strings.Contains(out, "coding-agent") || !strings.Contains(out, "execution unavailable") || !strings.Contains(out, "No inference request was made.") {
		t.Fatalf("list exit %d\n%s", code, out)
	}
	if strings.Contains(out, "sk-") {
		t.Fatal(out)
	}
	errOut := capture(t, &os.Stderr, func() {
		code = Run([]string{"agt", "agent", "run", "--server", ts.URL, "fix the tests"})
	})
	if code != 2 || !strings.Contains(errOut, "unavailable") {
		t.Fatalf("run exit %d\n%s", code, errOut)
	}
	for _, path := range paths {
		if path != "/healthz" && path != "/v1/agents" {
			t.Fatalf("path %s", path)
		}
	}
}
