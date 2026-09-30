package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOTLPPostsTrace(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	otlp := &OTLP{Endpoint: srv.URL, Client: srv.Client()}
	otlp.Emit(context.Background(), Event{
		TraceID: "0123456789abcdef0123456789abcdef",
		RunID:   "run_abc",
		Name:    "command.finished",
		Seq:     7,
	})
	if !strings.Contains(body, `"traceId":"0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(body, `"name":"command.finished"`) || !strings.Contains(body, "run_abc") {
		t.Fatalf("body = %s", body)
	}
}
