package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/version"
)

func TestIdentityIsOpenAndOmitsSecrets(t *testing.T) {
	ts := httptest.NewServer(api.New(nil, config.Config{
		Provider:    "openai",
		ModelName:   "local-repair",
		ModelAPIKey: "super-secret-value",
		ListenAddr:  "127.0.0.1:8787",
	}, "test-token-value1").Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/identity")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity %d %s", resp.StatusCode, body)
	}
	text := string(body)
	if strings.Contains(text, "super-secret-value") || strings.Contains(text, "test-token-value1") {
		t.Fatalf("identity leaked a secret: %s", text)
	}
	if !strings.Contains(text, `"product":"agt"`) || !strings.Contains(text, version.Product) || !strings.Contains(text, "local-repair") {
		t.Fatalf("identity %s", text)
	}

	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.Header.Get(version.ProductHeader) != version.Name || health.Header.Get(version.VersionHeader) != version.Product {
		t.Fatalf("headers %v", health.Header)
	}
	if health.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil store health %d", health.StatusCode)
	}
}
