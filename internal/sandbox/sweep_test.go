package sandbox

import (
	"testing"

	"github.com/csy20/agt/internal/run"
)

func TestShouldSweepLeavesLiveWork(t *testing.T) {
	live := []run.State{
		run.Created, run.Contextualising, run.Planning, run.Executing, run.Observing,
		run.Verifying, run.AwaitingApproval, run.Committing, run.FailedRetryable,
	}
	for _, state := range live {
		if ShouldSweep(state, true) {
			t.Fatalf("swept live state %s", state)
		}
	}
	idle := []run.State{run.Succeeded, run.FailedTerminal, run.Paused, run.PausedBudget, run.Cancelled}
	for _, state := range idle {
		if !ShouldSweep(state, true) {
			t.Fatalf("kept idle state %s", state)
		}
	}
	if !ShouldSweep(run.Executing, false) {
		t.Fatal("missing run was kept")
	}
}

func TestParseRunLabels(t *testing.T) {
	id, ok := ParseRunLabels(`{"agt-run":"run_abc","other":"x"}`)
	if !ok || id != "run_abc" {
		t.Fatalf("id %q ok %v", id, ok)
	}
	if _, ok := ParseRunLabels(`{"other":"x"}`); ok {
		t.Fatal("accepted a container without agt-run")
	}
	if _, ok := ParseRunLabels("not-json"); ok {
		t.Fatal("accepted invalid json")
	}
}
