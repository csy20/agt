package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestLeaseFenceStopsAStaleWorker(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "h-lease", "k-lease")
	first, err := st.ClaimJob(ctx, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != created.ID || first.Fence != 1 {
		t.Fatalf("first claim %+v", first)
	}
	if err := st.ExpireLease(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := st.ClaimJob(ctx, "worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Fence != 2 {
		t.Fatalf("second claim %+v", second)
	}
	for _, err := range []error{
		st.FinishJob(ctx, first.ID, "worker-a", first.Fence),
		st.ReleaseJob(ctx, first.ID, "worker-a", first.Fence),
		st.RenewLease(ctx, first.ID, "worker-a", first.Fence),
	} {
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale worker err %v", err)
		}
	}
	if err := st.RenewLease(ctx, second.ID, "worker-b", second.Fence); err != nil {
		t.Fatal(err)
	}
	finished, err := st.FinishIfQuiescent(ctx, second.ID, "worker-b", second.Fence)
	if err != nil || finished {
		t.Fatalf("finished %v err %v while CREATED", finished, err)
	}
	if _, err := st.Transition(ctx, created.ID, created.StateVersion, run.Paused, "test"); err != nil {
		t.Fatal(err)
	}
	finished, err = st.FinishIfQuiescent(ctx, second.ID, "worker-b", second.Fence)
	if err != nil || !finished {
		t.Fatalf("finished %v err %v", finished, err)
	}
	if _, err := st.FinishIfQuiescent(ctx, second.ID, "worker-b", second.Fence); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second finish %v", err)
	}
	if _, err := st.ClaimJob(ctx, "worker-c"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim after finish %v", err)
	}
	if err := st.Enqueue(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	job, err := st.ActiveJob(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Fence != 0 {
		t.Fatalf("requeued fence %d", job.Fence)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = st.Enqueue(ctx, created.ID)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ActiveJob(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIsSafeWhenCalledTogether(t *testing.T) {
	st := testutil.Open(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = st.Migrate(context.Background())
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsageRecordsModelProfileAndTestRuns(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	prof, err := profile.Resolve(profile.Request{
		Name: profile.NamePython,
		Argv: []string{"python", "-m", "unittest", "tests.test_retry"},
	}, profile.Images{Python: "agt-sandbox:python"}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := st.CreateRun(ctx, store.CreateInput{
		IdempotencyKey: "k-usage",
		RequestHash:    "h-usage",
		Goal:           "goal",
		RepoPath:       "/tmp/payrepo",
		BudgetCents:    10,
		MaxSteps:       4,
		WallSeconds:    30,
		Provider:       "openai",
		ProfileJSON:    raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Transition(ctx, created.ID, created.StateVersion, run.Contextualising, "test")
	if err != nil {
		t.Fatal(err)
	}
	current, err = st.Transition(ctx, created.ID, current.StateVersion, run.Planning, "test")
	if err != nil {
		t.Fatal(err)
	}
	current, err = st.CommitModelIntent(ctx, created.ID, current.StateVersion, store.Intent{
		Kind:         "tool",
		Tool:         "run_test",
		Canonical:    []byte(`{}`),
		ArgsHash:     "hash",
		Decision:     "allow",
		CommandID:    "cmd_usage",
		ToolCallID:   "call_usage",
		Text:         "run the test",
		ModelName:    "configured-model",
		InputTokens:  10,
		OutputTokens: 4,
		CostMicros:   12,
		Model:        "openai",
		Attempts:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishCommand(ctx, "cmd_usage", "succeeded", map[string]any{"exit_code": 1, "output": "fail"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListUsage(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Usage{}
	for _, row := range rows {
		got[row.Kind] = row
	}
	call := got["model.call"]
	if call.ModelName != "configured-model" || call.ProfileName != "python" || call.CostMicros != 12 {
		t.Fatalf("model.call %+v", call)
	}
	if got["model.http_attempts"].Quantity != 2 || got["model.http_attempts"].ProfileName != "python" {
		t.Fatalf("attempts %+v", got["model.http_attempts"])
	}
	if got["sandbox.test_runs"].Quantity != 1 || got["sandbox.test_runs"].ProfileName != "python" {
		t.Fatalf("tests %+v", got["sandbox.test_runs"])
	}
	if current.State != run.Executing {
		t.Fatalf("state %s", current.State)
	}
}
