package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCommandsDoNotStartOrApply(t *testing.T) {
	repo := t.TempDir()
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(`{"steps":[{"finish":true,"text":"done"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var hits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/healthz" {
			writeTestHealth(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token-value1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"session":{"id":"ses_new","repo_path":"/repo","title":""}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			_, _ = io.WriteString(w, `{"sessions":[{"id":"ses_done","repo_path":"/repo","title":"retry"}]}`)
		case r.URL.Path == "/v1/sessions/ses_done/open":
			_, _ = io.WriteString(w, `{"session":{"id":"ses_done","repo_path":"/repo","title":"retry"},"messages":[{"seq":1,"role":"user","kind":"user","content":"first goal","run_id":"run_done"},{"seq":2,"role":"note","kind":"pending","content":"Its edits are absent from the new snapshot.","run_id":"run_done"}],"runs":[{"id":"run_done","state":"SUCCEEDED"}],"active_run_id":"","pending":[{"run_id":"run_done","change":"verified","note":"absent"}]}`)
		case r.URL.Path == "/v1/sessions/ses_live/open":
			_, _ = io.WriteString(w, `{"session":{"id":"ses_live","repo_path":"/repo"},"messages":[{"seq":1,"role":"user","kind":"user","content":"live goal","run_id":"run_live"}],"runs":[{"id":"run_live","state":"PAUSED"}],"active_run_id":"run_live","pending":[]}`)
		case r.URL.Path == "/v1/sessions/ses_done" || r.URL.Path == "/v1/sessions/ses_live":
			_, _ = io.WriteString(w, `{"session":{"id":"ses_shown","repo_path":"/repo"},"messages":[{"seq":1,"role":"user","kind":"user","content":"shown goal"}],"active_run_id":"","pending":[]}`)
		case r.URL.Path == "/v1/sessions/ses_other/open":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"session is bound to /kept and was not redirected to /other","repo_path":"/kept"}`)
		case r.URL.Path == "/v1/sessions/ses_live/messages":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"a run is already active (run_live). This prompt was not stored and the run goal was not changed","active_run_id":"run_live"}`)
		case r.URL.Path == "/v1/sessions/ses_done/messages":
			_, _ = io.WriteString(w, `{"session_id":"ses_done","run_id":"run_done","state":"SUCCEEDED","created":false,"client_message_id":"msg_1"}`)
		case r.URL.Path == "/v1/runs/run_live/events":
			_, _ = io.WriteString(w, "id: 1\nevent: state.changed\ndata: {\"from\":\"CREATED\",\"to\":\"PAUSED\"}\n\n")
		case r.URL.Path == "/v1/runs/run_live":
			_, _ = io.WriteString(w, `{"id":"run_live","state":"PAUSED","repo_path":"/repo"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")

	code, out, errOut := captureBoth(t, func() int {
		return Run([]string{"agt", "session", "new", "--server", ts.URL, "--repo", repo})
	})
	if code != 0 || !strings.Contains(out, "ses_new") || !strings.Contains(out, "No run was started") {
		t.Fatalf("new %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "list", "--server", ts.URL, "--repo", repo})
	})
	if code != 0 || !strings.Contains(out, "ses_done") || !strings.Contains(out, "retry") {
		t.Fatalf("list %d\n%s\n%s", code, out, errOut)
	}
	beforeOpen := len(hits)
	code, out, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "open", "ses_done", "--server", ts.URL, "--repo", repo})
	})
	if code != 0 || !strings.Contains(out, "first goal") || !strings.Contains(out, "seq") && !strings.Contains(out, "1  user") {
		t.Fatalf("open done %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "No run was started. Patches were not applied.") || strings.Contains(out, "attaching") {
		t.Fatalf("open done started work\n%s\n%s", out, errOut)
	}
	if !strings.Contains(out, "absent from the new snapshot") && !strings.Contains(out, "change=verified") {
		t.Fatalf("open hid the pending patch\n%s", out)
	}
	for _, hit := range hits[beforeOpen:] {
		if strings.Contains(hit, "/events") || strings.Contains(hit, "/apply") {
			t.Fatalf("completed open called %s", hit)
		}
	}
	code, out, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "show", "ses_done", "--server", ts.URL, "--repo", repo})
	})
	if code != 0 || !strings.Contains(out, "shown goal") || strings.Contains(strings.Join(hits, "\n"), "/v1/runs/") {
		t.Fatalf("show %d\n%s\n%s\nhits %s", code, out, errOut, hits)
	}
	code, out, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "open", "ses_live", "--server", ts.URL, "--repo", repo})
	})
	if code != 1 || !strings.Contains(out, "attaching. This does not start a run or apply a patch.") || !strings.Contains(out, "run_live") {
		t.Fatalf("open live %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(strings.Join(hits, "\n"), "GET /v1/runs/run_live/events") {
		t.Fatalf("active open did not attach %s", hits)
	}
	if strings.Contains(strings.Join(hits, "\n"), "/apply") {
		t.Fatal("open applied a patch")
	}
	code, out, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "submit", "ses_done", "--server", ts.URL, "--repo", repo, "--goal", "first goal", "--message-id", "msg_1", "--provider", "fake", "--script", script, "--profile", "python", "--test-arg", "python", "--test-arg", "-c", "--test-arg", "pass"})
	})
	if code != 0 || !strings.Contains(out, "existing run run_done") || !strings.Contains(out, "did not create another run") {
		t.Fatalf("replay %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "client_message_id msg_1") || !strings.Contains(errOut, "scripted provider, not a live model") {
		t.Fatalf("provenance missing\n%s", errOut)
	}
	code, _, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "submit", "ses_live", "--server", ts.URL, "--repo", repo, "--goal", "new goal", "--message-id", "msg_2", "--provider", "fake", "--script", script, "--profile", "python", "--test-arg", "python", "--test-arg", "-c", "--test-arg", "pass"})
	})
	if code == 0 || !strings.Contains(errOut, "run_live") || !strings.Contains(errOut, "was not changed") {
		t.Fatalf("busy %d\n%s", code, errOut)
	}
	code, _, errOut = captureBoth(t, func() int {
		return Run([]string{"agt", "session", "open", "ses_other", "--server", ts.URL, "--repo", repo})
	})
	if code == 0 || !strings.Contains(errOut, "/kept") || !strings.Contains(errOut, "was not redirected") {
		t.Fatalf("mismatch %d\n%s", code, errOut)
	}
}

func captureBoth(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC := make(chan string, 1)
	errC := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(outR)
		outC <- string(body)
	}()
	go func() {
		body, _ := io.ReadAll(errR)
		errC <- string(body)
	}()
	code := fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	return code, <-outC, <-errC
}
