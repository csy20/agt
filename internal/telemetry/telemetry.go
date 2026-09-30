package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Event is one exported span. The database event log remains the audit source.
type Event struct {
	TraceID string
	RunID   string
	Name    string
	Seq     int64
	Attrs   map[string]string
}

type Emitter interface {
	Emit(context.Context, Event)
}

type Discard struct{}

func (Discard) Emit(context.Context, Event) {}

type Log struct{}

func (Log) Emit(_ context.Context, ev Event) {
	slog.Info("span", "trace_id", ev.TraceID, "run_id", ev.RunID, "name", ev.Name, "seq", ev.Seq)
}

type Multi []Emitter

func (m Multi) Emit(ctx context.Context, ev Event) {
	for _, e := range m {
		e.Emit(ctx, ev)
	}
}

// OTLP posts a minimal OTLP/HTTP JSON span. Export failure does not fail the run.
type OTLP struct {
	Endpoint string
	Client   *http.Client
}

func New(endpoint string) Emitter {
	if strings.TrimSpace(endpoint) == "" {
		return Log{}
	}
	return Multi{Log{}, &OTLP{Endpoint: endpoint, Client: &http.Client{Timeout: 2 * time.Second}}}
}

func (o *OTLP) Emit(ctx context.Context, ev Event) {
	if o == nil || o.Endpoint == "" || ev.TraceID == "" {
		return
	}
	body := payload(ev)
	endpoint := strings.TrimRight(o.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/v1/traces") {
		endpoint += "/v1/traces"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("otlp export failed", "err", err)
		return
	}
	resp.Body.Close()
}

func payload(ev Event) []byte {
	attrs := []map[string]any{
		{"key": "run_id", "value": map[string]any{"stringValue": ev.RunID}},
		{"key": "event_name", "value": map[string]any{"stringValue": ev.Name}},
	}
	for key, value := range ev.Attrs {
		attrs = append(attrs, map[string]any{
			"key":   key,
			"value": map[string]any{"stringValue": value},
		})
	}
	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	spanID := fmt.Sprintf("%016x", uint64(ev.Seq))
	doc := map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{
				"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "agtd"}},
				},
			},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "agt"},
				"spans": []any{map[string]any{
					"traceId":           ev.TraceID,
					"spanId":            spanID,
					"name":              ev.Name,
					"kind":              1,
					"startTimeUnixNano": now,
					"endTimeUnixNano":   now,
					"attributes":        attrs,
				}},
			}},
		}},
	}
	body, _ := json.Marshal(doc)
	return body
}
