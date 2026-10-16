package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/agent"
	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/version"
)

func TestAgentScreenSeparatesModelAPIsFromCodingAgents(t *testing.T) {
	const secret = "sk-test-secret-value"
	t.Setenv("AGT_MODEL_API_KEY", secret)
	srv := api.New(nil, config.Config{
		Provider:    "fake",
		ModelName:   "scripted-model",
		ModelAPIKey: secret,
		ListenAddr:  "127.0.0.1:1",
	}, "token-value")
	srv.AgentProbe = func(name string) (string, bool, error) {
		if name == "codex" {
			return "codex-cli 0.162.1", true, nil
		}
		return "", false, nil
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(health.Body)
	health.Body.Close()
	if health.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "database unavailable") {
		t.Fatalf("health %d %q", health.StatusCode, body)
	}
	if health.Header.Get(version.CapabilitiesHeader) != "sessions,models" {
		t.Fatalf("capabilities %q", health.Header.Get(version.CapabilitiesHeader))
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/agents", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token-value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("agents %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "token-value") {
		t.Fatalf("screen disclosed a secret: %s", raw)
	}
	var screen agent.Screen
	if err := json.Unmarshal(raw, &screen); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Notice, "No inference request was made") || !strings.Contains(screen.Notice, "No coding agent was started") {
		t.Fatal(screen.Notice)
	}
	var sawModel, sawCodex, sawClaude bool
	for _, entry := range screen.Entries {
		if entry.LiveInference {
			t.Fatalf("live row %+v", entry)
		}
		switch entry.Kind {
		case agent.KindModelAPI:
			sawModel = true
			if !entry.Scripted || entry.Execution != "scripted" || entry.Name != "default" {
				t.Fatalf("model row %+v", entry)
			}
		case agent.KindCodingAgent:
			if entry.Execution != "unavailable" || entry.Mutation != "unavailable" || entry.Authentication != "not-probed" {
				t.Fatalf("agent row %+v", entry)
			}
			if entry.Name == "codex" && entry.Installed && entry.RuntimeSupported {
				sawCodex = true
			}
			if entry.Name == "claude" && !entry.Installed {
				sawClaude = true
			}
		default:
			t.Fatalf("kind %s", entry.Kind)
		}
	}
	if !sawModel || !sawCodex || !sawClaude {
		t.Fatalf("entries %+v", screen.Entries)
	}
}
