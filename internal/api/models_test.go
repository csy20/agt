package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/testutil"
)

func TestModelSelectionDoesNotInfer(t *testing.T) {
	const secret = "sk-test-secret-value"
	infer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("inference request %s %s", r.Method, r.URL.Path)
		http.Error(w, "model", http.StatusInternalServerError)
	}))
	t.Cleanup(infer.Close)

	t.Setenv("AGT_PROVIDER", "fake")
	t.Setenv("AGT_MODEL_NAME", "scripted-model")
	t.Setenv("AGT_MODEL_API_KEY", secret)
	t.Setenv("AGT_CLOUD_KEY", secret)
	t.Setenv("AGT_MODELS", "local,cloud,bare")
	t.Setenv("AGT_MODEL_LOCAL_ADAPTER", "openai")
	t.Setenv("AGT_MODEL_LOCAL_ENDPOINT", infer.URL+"/v1")
	t.Setenv("AGT_MODEL_LOCAL_MODEL", "local-repair")
	t.Setenv("AGT_MODEL_LOCAL_INPUT_USD_PER_M", "3")
	t.Setenv("AGT_MODEL_LOCAL_OUTPUT_USD_PER_M", "0")
	t.Setenv("AGT_MODEL_CLOUD_ADAPTER", "openai")
	t.Setenv("AGT_MODEL_CLOUD_ENDPOINT", "https://example.test/v1")
	t.Setenv("AGT_MODEL_CLOUD_MODEL", "cloud-model")
	t.Setenv("AGT_MODEL_CLOUD_API_KEY_ENV", "AGT_CLOUD_KEY")
	t.Setenv("AGT_MODEL_BARE_ADAPTER", "openai")
	t.Setenv("AGT_MODEL_BARE_MODEL", "bare-model")

	data := t.TempDir()
	cfg := config.Config{
		Provider:    "fake",
		DataDir:     data,
		ModelAPIKey: secret,
		ModelName:   "scripted-model",
		Models:      config.LoadModels(),
	}
	st := testutil.Open(t)
	ts := httptest.NewServer(api.New(st, cfg, testToken).Handler())
	t.Cleanup(ts.Close)

	identResp, err := http.Get(ts.URL + "/v1/identity")
	if err != nil {
		t.Fatal(err)
	}
	identBody, _ := io.ReadAll(identResp.Body)
	identResp.Body.Close()
	assertNoSecret(t, "identity", string(identBody))
	if !strings.Contains(string(identBody), `"model_config":"default"`) || !strings.Contains(string(identBody), `"scripted":true`) {
		t.Fatalf("identity %s", identBody)
	}

	listed := getJSON(t, ts.URL+"/v1/models")
	listedRaw := mustJSON(t, listed)
	assertNoSecret(t, "models", listedRaw)
	if !strings.Contains(listedRaw, "AGT_CLOUD_KEY") || !strings.Contains(listedRaw, "was not given the hosted default") {
		t.Fatalf("models %s", listedRaw)
	}
	if strings.Contains(listedRaw, "https://api.openai.com") {
		t.Fatalf("named catalog advertised the hosted default: %s", listedRaw)
	}

	bare := postJSON(t, ts.URL+"/v1/models/select", map[string]any{"name": "bare"}, "select-bare")
	bareBody, _ := io.ReadAll(bare.body)
	bare.body.Close()
	if bare.status != http.StatusBadRequest || !strings.Contains(string(bareBody), "was not given the hosted default") {
		t.Fatalf("bare select %d %s", bare.status, bareBody)
	}
	if _, err := os.Stat(filepath.Join(data, "model-default")); !os.IsNotExist(err) {
		t.Fatal("invalid selection was stored")
	}

	chosen := postJSON(t, ts.URL+"/v1/models/select", map[string]any{"name": "local"}, "select-local")
	chosenBody, _ := io.ReadAll(chosen.body)
	chosen.body.Close()
	if chosen.status != http.StatusOK || !strings.Contains(string(chosenBody), "No inference request was made.") {
		t.Fatalf("select %d %s", chosen.status, chosenBody)
	}
	assertNoSecret(t, "select", string(chosenBody))
	info, err := os.Stat(filepath.Join(data, "model-default"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("selection file %v %v", info, err)
	}

	mismatch := postRun(t, ts.URL, map[string]any{
		"goal": "mismatch", "repo_path": testutil.Fixture(t), "provider": "fake",
		"model_config": "local", "profile": pythonProfile(),
	}, "idem-mismatch")
	if mismatch.status != http.StatusBadRequest || !strings.Contains(mismatch.body, "does not match") {
		t.Fatalf("mismatch %d %s", mismatch.status, mismatch.body)
	}
	leaked := postRun(t, ts.URL, map[string]any{
		"goal": "key", "repo_path": testutil.Fixture(t), "api_key": secret, "profile": pythonProfile(),
	}, "idem-key")
	if leaked.status != http.StatusBadRequest || !strings.Contains(leaked.body, "not accepted") || strings.Contains(leaked.body, secret) {
		t.Fatalf("client key %d", leaked.status)
	}

	created := postSession(t, ts.URL, testutil.Fixture(t))
	if created.modelConfig != "local" {
		t.Fatalf("session copied %q", created.modelConfig)
	}
	first := postSessionMessage(t, ts.URL, created.id, map[string]any{
		"goal": "use the session configuration", "content": "use the session configuration",
		"repo_path": testutil.Fixture(t), "profile": pythonProfile(),
		"client_message_id": "msg-local", "budget_cents": 100, "max_steps": 0,
	})
	if first.status != http.StatusAccepted {
		t.Fatalf("session run %d %s", first.status, first.body)
	}
	localView := getJSON(t, ts.URL+"/v1/runs/"+first.runID)
	if localView["model_config"] != "local" || localView["model_name"] != "local-repair" || localView["model_endpoint"] != infer.URL+"/v1" || localView["scripted"] != false || localView["pricing_recorded"] != true {
		t.Fatalf("snapshot %#v", localView)
	}
	if localView["input_per_million"] != float64(3_000_000) || localView["output_per_million"] != float64(0) {
		t.Fatalf("prices %#v %#v", localView["input_per_million"], localView["output_per_million"])
	}

	back := postJSON(t, ts.URL+"/v1/models/select", map[string]any{"name": "default"}, "select-default")
	backBody, _ := io.ReadAll(back.body)
	back.body.Close()
	if back.status != http.StatusOK {
		t.Fatalf("reselect %d %s", back.status, backBody)
	}
	again := getJSON(t, ts.URL+"/v1/runs/"+first.runID)
	if again["model_config"] != "local" || again["model_endpoint"] != infer.URL+"/v1" {
		t.Fatalf("run moved after reselection %#v", again)
	}
	sessionView := getJSON(t, ts.URL+"/v1/sessions/"+created.id)
	sess, _ := sessionView["session"].(map[string]any)
	if sess["model_config"] != "local" {
		t.Fatalf("session moved %#v", sessionView["session"])
	}

	next := postSession(t, ts.URL, testutil.Fixture(t))
	if next.modelConfig != "default" {
		t.Fatalf("new session %q", next.modelConfig)
	}
	secretGoal := "repair using " + secret
	second := postSessionMessage(t, ts.URL, next.id, map[string]any{
		"goal": secretGoal, "content": secretGoal, "repo_path": testutil.Fixture(t),
		"profile": pythonProfile(), "script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"client_message_id": "msg-default", "budget_cents": 100, "max_steps": 0,
	})
	if second.status != http.StatusAccepted {
		t.Fatalf("second run %d %s", second.status, second.body)
	}
	secondView := getJSON(t, ts.URL+"/v1/runs/"+second.runID)
	if secondView["model_config"] != "default" || secondView["scripted"] != true || secondView["model_name"] != "scripted-model" {
		t.Fatalf("second snapshot %#v", secondView)
	}
	if again["model_endpoint"] != getJSON(t, ts.URL+"/v1/runs/"+first.runID)["model_endpoint"] {
		t.Fatal("first run endpoint changed")
	}
	runRaw := mustJSON(t, secondView)
	assertNoSecret(t, "run", runRaw)
	events := getBytes(t, ts.URL+"/v1/runs/"+second.runID+"/events")
	assertNoSecret(t, "events", string(events))
	if !strings.Contains(string(events), "[redacted]") {
		t.Fatal("stored goal kept the credential")
	}
}

type createdSession struct {
	id          string
	modelConfig string
}

func postSession(t *testing.T, base, repo string) createdSession {
	t.Helper()
	resp := postJSON(t, base+"/v1/sessions", map[string]any{"repo_path": repo, "title": "models"}, "session-new")
	defer resp.body.Close()
	var body struct {
		Session struct {
			ID          string `json:"id"`
			ModelConfig string `json:"model_config"`
		} `json:"session"`
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.body)
	if resp.status != http.StatusCreated {
		t.Fatalf("session %d %s", resp.status, raw)
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Session.ID == "" {
		t.Fatalf("session %s", raw)
	}
	return createdSession{id: body.Session.ID, modelConfig: body.Session.ModelConfig}
}

func postSessionMessage(t *testing.T, base, id string, body map[string]any) posted {
	t.Helper()
	resp := postJSON(t, base+"/v1/sessions/"+id+"/messages", body, "msg-"+id)
	defer resp.body.Close()
	raw, _ := io.ReadAll(resp.body)
	var decoded struct {
		RunID string `json:"run_id"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &decoded)
	text := string(raw)
	if decoded.Error != "" {
		text = decoded.Error
	}
	return posted{status: resp.status, body: text, runID: decoded.RunID}
}

func assertNoSecret(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "sk-test-secret-value") {
		t.Fatalf("credential leaked in %s", where)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
