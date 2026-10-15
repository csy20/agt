package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestPauseResumeCancelAndCleanup(t *testing.T) {
	st := testutil.Open(t)
	srv := api.New(st, config.Config{Provider: "fake"}, testToken)
	var cleaned int
	srv.Cleanup = func(context.Context) (int, error) {
		cleaned++
		return 2, nil
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	created := mustStoreRun(t, st, "k-life")
	current := advance(t, st, created, run.Contextualising, run.Planning)
	paused := postLife(t, ts.URL+"/v1/runs/"+current.ID+"/pause", nil)
	if paused.status != http.StatusOK || !strings.Contains(paused.body, `"state":"PAUSED"`) {
		t.Fatalf("pause %d %s", paused.status, paused.body)
	}
	resumed := postLife(t, ts.URL+"/v1/runs/"+current.ID+"/resume", nil)
	if resumed.status != http.StatusOK || !strings.Contains(resumed.body, `"state":"PLANNING"`) {
		t.Fatalf("resume %d %s", resumed.status, resumed.body)
	}
	if _, err := st.ActiveJob(context.Background(), current.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := postLife(t, ts.URL+"/v1/runs/"+current.ID+"/cancel", nil)
	if cancelled.status != http.StatusOK || !strings.Contains(cancelled.body, `"state":"CANCELLED"`) {
		t.Fatalf("cancel %d %s", cancelled.status, cancelled.body)
	}
	again := postLife(t, ts.URL+"/v1/runs/"+current.ID+"/pause", nil)
	if again.status != http.StatusConflict {
		t.Fatalf("pause cancelled %d %s", again.status, again.body)
	}

	done := mustStoreRun(t, st, "k-done")
	done = advance(t, st, done, run.Contextualising, run.Planning, run.Verifying, run.Succeeded)
	blocked := postLife(t, ts.URL+"/v1/runs/"+done.ID+"/pause", nil)
	if blocked.status != http.StatusConflict {
		t.Fatalf("pause succeeded %d %s", blocked.status, blocked.body)
	}

	budget := mustStoreRun(t, st, "k-budget")
	budget = advance(t, st, budget, run.PausedBudget)
	low := postLife(t, ts.URL+"/v1/runs/"+budget.ID+"/budget-resume", []byte(`{"budget_cents":1}`))
	if low.status != http.StatusBadRequest {
		t.Fatalf("low budget %d %s", low.status, low.body)
	}
	unknown := postLife(t, ts.URL+"/v1/runs/"+budget.ID+"/budget-resume", []byte(`{"repo_path":"/tmp"}`))
	if unknown.status != http.StatusBadRequest {
		t.Fatalf("unknown field %d %s", unknown.status, unknown.body)
	}
	raised := postLife(t, ts.URL+"/v1/runs/"+budget.ID+"/budget-resume", []byte(`{"budget_cents":50}`))
	if raised.status != http.StatusOK || !strings.Contains(raised.body, `"state":"PLANNING"`) || !strings.Contains(raised.body, `"budget_cents":50`) {
		t.Fatalf("budget resume %d %s", raised.status, raised.body)
	}

	swept := postLife(t, ts.URL+"/v1/cleanup", nil)
	if swept.status != http.StatusOK || !strings.Contains(swept.body, `"removed":2`) || cleaned != 1 {
		t.Fatalf("cleanup %d %s calls %d", swept.status, swept.body, cleaned)
	}
}

func mustStoreRun(t *testing.T, st *store.Store, key string) store.Run {
	t.Helper()
	created, _, err := st.CreateRun(context.Background(), store.CreateInput{
		IdempotencyKey: key,
		RequestHash:    key + "-hash",
		Goal:           "goal",
		RepoPath:       "/tmp/payrepo",
		BudgetCents:    10,
		MaxSteps:       4,
		WallSeconds:    30,
		Provider:       "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func postLife(t *testing.T, url string, body []byte) posted {
	t.Helper()
	if body == nil {
		body = []byte(`{}`)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return posted{status: resp.StatusCode, body: string(raw)}
}
