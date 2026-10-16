package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/config"
)

func TestModelCommandsDoNotInfer(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "chat") || r.URL.Path == "/v1/runs" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			writeTestHealth(w)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer test-token-value1" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"selected":"default","configs":[{"name":"default","adapter":"fake","model":"local-repair","scripted":true,"valid":true,"selected":true,"input_usd_per_m":"0","output_usd_per_m":"0","credential_env":"AGT_MODEL_API_KEY","credential_set":false},{"name":"bare","adapter":"openai","valid":false,"problem":"openai configuration bare requires an endpoint and was not given the hosted default"}]}`)
		case "/v1/models/select":
			var req struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name != "local" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"selected":"local","config":{"name":"local","adapter":"openai","model":"local-repair","scripted":false,"valid":true},"notice":"No inference request was made."}`)
		case "/v1/sessions/ses_1/model":
			fmt.Fprintf(w, `{"config":{"name":"local","adapter":"fake","model":"local-repair","scripted":true,"valid":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", "test-token-value1")

	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "model", "list", "--server", ts.URL})
	})
	if code != 0 || !strings.Contains(out, "scripted") || !strings.Contains(out, "No inference request was made.") || !strings.Contains(out, "was not given the hosted default") {
		t.Fatalf("list exit %d\n%s", code, out)
	}
	if strings.Contains(out, "sk-") {
		t.Fatal("list printed a credential")
	}
	out = captureStdout(t, func() {
		code = Run([]string{"agt", "model", "select", "--server", ts.URL, "local"})
	})
	if code != 0 || !strings.Contains(out, "selected local") || !strings.Contains(out, "No inference request was made.") {
		t.Fatalf("select exit %d\n%s", code, out)
	}
	out = captureStdout(t, func() {
		code = Run([]string{"agt", "model", "select", "--server", ts.URL, "--session", "ses_1", "local"})
	})
	if code != 0 || !strings.Contains(out, "session ses_1") || !strings.Contains(out, "scripted") {
		t.Fatalf("session select exit %d\n%s", code, out)
	}
	for _, path := range paths {
		if path == "/v1/runs" || strings.Contains(path, "chat") {
			t.Fatalf("inference path %s", path)
		}
	}
}

func TestProbeModelLabelsScriptedConfigsAndHidesCredentials(t *testing.T) {
	const secret = "sk-test-secret-value"
	t.Setenv("AGT_PROVIDER", "fake")
	t.Setenv("AGT_MODEL_NAME", "local-repair")
	t.Setenv("AGT_MODEL_API_KEY", secret)
	t.Setenv("AGT_MODELS", "bare")
	t.Setenv("AGT_MODEL_BARE_ADAPTER", "openai")
	t.Setenv("AGT_MODEL_BARE_MODEL", "bare-model")
	cfg := config.Load()
	failed := probeModel(cfg)
	if failed.OK || strings.Contains(failed.Detail, secret) || !strings.Contains(failed.Detail, "was not given the hosted default") {
		t.Fatalf("%+v", failed)
	}
	t.Setenv("AGT_MODELS", "")
	ok := probeModel(config.Load())
	if !ok.OK || strings.Contains(ok.Detail, secret) || !strings.Contains(ok.Detail, "scripted") || !strings.Contains(ok.Detail, "local-repair") {
		t.Fatalf("%+v", ok)
	}
}
