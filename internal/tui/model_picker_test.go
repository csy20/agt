package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
)

func TestModelPickerDoesNotStartARun(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/runs" || strings.Contains(r.URL.Path, "chat") {
			t.Errorf("inference request %s", r.URL.Path)
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"selected":"default","configs":[{"name":"default","adapter":"fake","model":"local-repair","scripted":true,"valid":true,"selected":true},{"name":"local","adapter":"openai","model":"local-repair","scripted":false,"valid":true}]}`)
		case "/v1/models/select":
			_, _ = io.WriteString(w, `{"selected":"local","config":{"name":"local","adapter":"openai","model":"local-repair","scripted":false,"valid":true},"notice":"No inference request was made."}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	m := newModel(Options{Repo: t.TempDir()})
	m.client = client.New(ts.URL, "test-token-value1")
	next, cmd := m.submit("/model")
	if cmd == nil {
		t.Fatal("picker did not load")
	}
	listed, cmd := next.(model).Update(cmd())
	got := listed.(model)
	if cmd != nil || got.snap.View != "picker" || got.pickerKind != "model" || !strings.Contains(got.View(), "scripted") {
		t.Fatalf("picker %s %q", got.snap.View, got.View())
	}
	left, _ := got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if left.(model).snap.View == "picker" {
		t.Fatal("esc stayed on the model picker")
	}
	again, cmd := left.(model).submit("/model")
	listed, _ = again.(model).Update(cmd())
	got = listed.(model)
	got.snap.PickerAt = 1
	chosen, cmd := got.openPicker()
	if cmd == nil {
		t.Fatal("enter did not select")
	}
	done, cmd := chosen.(model).Update(cmd())
	final := done.(model)
	if cmd != nil || final.modelConfig != "local" || final.opts.Provider != "openai" || !strings.Contains(final.snap.Notice, "No inference request was made.") {
		t.Fatalf("selected %q provider %q notice %q", final.modelConfig, final.opts.Provider, final.snap.Notice)
	}
	if strings.Contains(final.View(), "sk-") {
		t.Fatal("picker showed a credential")
	}
	for _, path := range paths {
		if path == "/v1/runs" || strings.Contains(path, "chat") {
			t.Fatalf("path %s", path)
		}
	}

	demo, cmd := newModel(Options{Demo: true}).submit("/model cloud")
	if cmd != nil || !strings.Contains(demo.(model).snap.Notice, "No inference request was made.") {
		t.Fatalf("demo %q cmd %v", demo.(model).snap.Notice, cmd)
	}

	emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"configs":[]}`)
	}))
	t.Cleanup(emptySrv.Close)
	empty := newModel(Options{Repo: t.TempDir()})
	empty.client = client.New(emptySrv.URL, "test-token-value1")
	pending, cmd := empty.submit("/model")
	shown, _ := pending.(model).Update(cmd())
	if shown.(model).snap.View == "picker" || !strings.Contains(shown.(model).snap.Notice, "No model configurations") {
		t.Fatalf("empty %s %q", shown.(model).snap.View, shown.(model).snap.Notice)
	}
}
