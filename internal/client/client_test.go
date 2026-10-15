package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/version"
)

func TestCreateAndApproveStayOnTheDaemonContract(t *testing.T) {
	var created map[string]any
	var approved map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/identity" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"product": version.Name, "version": version.Product, "provider": "openai", "model": "local-repair",
			})
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			if err := json.Unmarshal(body, &created); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"run_id":"run_1","state":"CREATED"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_1/approvals":
			if err := json.Unmarshal(body, &approved); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"id":"run_1","state":"EXECUTING"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-token")
	id, err := c.Identity(context.Background())
	if err != nil || id.Model != "local-repair" {
		t.Fatalf("identity %+v %v", id, err)
	}
	got, err := c.CreateRun(context.Background(), CreateRequest{
		Goal: "make the retry test pass", RepoPath: "/tmp/payrepo", Provider: "openai",
		BudgetCents: 100, Interaction: "plan",
		Profile: profile.Request{Name: "python", Argv: []string{"python", "-m", "unittest", "tests.test_retry"}, Workdir: "."},
	})
	if err != nil || got.RunID != "run_1" {
		t.Fatalf("create %+v %v", got, err)
	}
	if created["interaction"] != "plan" {
		t.Fatalf("interaction %#v", created["interaction"])
	}
	raw, _ := json.Marshal(created["profile"])
	if strings.Contains(string(raw), "image") {
		t.Fatalf("profile included an image: %s", raw)
	}
	if _, err := c.Approve(context.Background(), "run_1", "cmd_1", "abc123", "approved"); err != nil {
		t.Fatal(err)
	}
	if approved["args_hash"] != "abc123" || approved["command_id"] != "cmd_1" || approved["decision"] != "approved" {
		t.Fatalf("approval %#v", approved)
	}
}

func TestEventsSkipHeartbeats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Fatal("missing token")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": heartbeat\n\nid: 3\nevent: assistant.delta\ndata: {\"text\":\"Looking\"}\n\n")
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-token")
	var got []Event
	err := c.ReadEvents(context.Background(), "run_1", 0, func(ev Event) error {
		got = append(got, ev)
		return nil
	})
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Seq != 3 || got[0].Type != "assistant.delta" || !strings.Contains(string(got[0].Data), "Looking") {
		t.Fatalf("events %+v err %v", got, err)
	}
}
