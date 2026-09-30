package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestIllegalTransitionLeavesVersion(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "h1", "k-illegal")
	_, err := st.Transition(ctx, created.ID, created.StateVersion, run.Executing, "test")
	if err == nil {
		t.Fatal("CREATED -> EXECUTING was accepted")
	}
	got, err := st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != run.Created || got.StateVersion != 1 {
		t.Fatalf("state %s version %d", got.State, got.StateVersion)
	}
}

func TestCompareAndSwapOneWinner(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "h-cas", "k-cas")
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = st.Transition(ctx, created.ID, created.StateVersion, run.Contextualising, "racer")
		}(i)
	}
	close(start)
	wg.Wait()
	wins, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins %d conflicts %d", wins, conflicts)
	}
	got, err := st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != run.Contextualising || got.StateVersion != 2 {
		t.Fatalf("state %s version %d", got.State, got.StateVersion)
	}
}

func TestConcurrentEventsAreContiguous(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "h-ev", "k-ev")
	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []error
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := st.AppendEvent(ctx, created.ID, "note", "test", "writer", map[string]any{"i": i})
			if err != nil {
				mu.Lock()
				failed = append(failed, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for _, err := range failed {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	events, err := st.EventsAfter(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n+1 {
		t.Fatalf("events %d", len(events))
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("sequence %d at index %d", ev.Seq, i)
		}
	}
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	in := store.CreateInput{
		IdempotencyKey: "same-key",
		RequestHash:    "hash-a",
		Goal:           "first",
		RepoPath:       "/tmp/payrepo",
		BudgetCents:    10,
		MaxSteps:       4,
		WallSeconds:    30,
		Provider:       "fake",
	}
	first, created, err := st.CreateRun(ctx, in)
	if err != nil || !created {
		t.Fatalf("create %v created %v", err, created)
	}
	second, created, err := st.CreateRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("replay created=%v id %s vs %s", created, second.ID, first.ID)
	}
	in.RequestHash = "hash-b"
	in.Goal = "different"
	if _, _, err := st.CreateRun(ctx, in); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("conflict err %v", err)
	}
}

func TestPendingCommandStoresNullResult(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "h-cmd", "k-cmd")
	cur, err := st.Transition(ctx, created.ID, created.StateVersion, run.Contextualising, "test")
	if err != nil {
		t.Fatal(err)
	}
	cur, err = st.Transition(ctx, cur.ID, cur.StateVersion, run.Planning, "test")
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.CommitModelIntent(ctx, cur.ID, cur.StateVersion, store.Intent{
		Kind:         "tool",
		Tool:         "run_test",
		Canonical:    []byte(`{}`),
		ArgsHash:     "abc",
		Decision:     "allow",
		CommandID:    "cmd_pending",
		InputTokens:  10,
		OutputTokens: 4,
		CostMicros:   12,
		Model:        "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != run.Executing {
		t.Fatalf("state %s", out.State)
	}
	cmd, err := st.OpenCommand(ctx, cur.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.CommandID != "cmd_pending" || cmd.State != "pending" {
		t.Fatalf("command %+v", cmd)
	}
	total, err := st.UsageTotal(ctx, cur.ID)
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 {
		t.Fatalf("usage %d", total)
	}
}

func mustCreate(t *testing.T, st *store.Store, hash, key string) store.Run {
	t.Helper()
	created, isNew, err := st.CreateRun(context.Background(), store.CreateInput{
		IdempotencyKey: key,
		RequestHash:    hash,
		Goal:           "goal",
		RepoPath:       "/tmp/payrepo",
		BudgetCents:    10,
		MaxSteps:       4,
		WallSeconds:    30,
		Provider:       "fake",
	})
	if err != nil || !isNew {
		t.Fatalf("create %v new %v", err, isNew)
	}
	return created
}
