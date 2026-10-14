package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
)

func TestMissingTokenCannotCreateOrApprove(t *testing.T) {
	const token = "test-token-value1"
	ts := httptest.NewServer(api.New(nil, config.Config{}, token).Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/v1/runs", "/v1/runs/run_1/approvals"} {
		resp, body := postNoAuth(t, ts.URL+path)
		if resp != http.StatusUnauthorized {
			t.Fatalf("%s status %d body %s", path, resp, body)
		}
		if strings.Contains(body, token) {
			t.Fatalf("unauthorized body echoed the token: %s", body)
		}
		if !strings.Contains(body, "unauthorized") {
			t.Fatalf("body %s", body)
		}
	}

	status, body := postAuth(t, ts.URL+"/v1/runs", "Bearer not-the-token-value")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong token %d %s", status, body)
	}
	if strings.Contains(body, token) || strings.Contains(body, "not-the-token-value") {
		t.Fatalf("body echoed a token: %s", body)
	}

	empty := httptest.NewServer(api.New(nil, config.Config{}, "").Handler())
	t.Cleanup(empty.Close)
	status, body = postAuth(t, empty.URL+"/v1/runs", "Bearer "+token)
	if status != http.StatusUnauthorized {
		t.Fatalf("empty server token %d %s", status, body)
	}
}

func TestBrowserOriginAndRemoteHostAreRejected(t *testing.T) {
	ts := httptest.NewServer(api.New(nil, config.Config{}, "test-token-value1").Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	status, body := send(t, req)
	if status != http.StatusForbidden {
		t.Fatalf("origin status %d %s", status, body)
	}

	for _, host := range []string{"192.168.1.20:8787", "example.com", "0.0.0.0:8787"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		status, body := send(t, req)
		if status != http.StatusForbidden {
			t.Fatalf("host %s status %d %s", host, status, body)
		}
	}
}

func postNoAuth(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return send(t, req)
}

func postAuth(t *testing.T, url, auth string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	return send(t, req)
}

func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}
