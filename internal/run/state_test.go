package run

import "testing"

func TestCanHappyPath(t *testing.T) {
	path := []State{Created, Contextualising, Planning, Executing, Observing, Verifying, Committing, Succeeded}
	for i := 0; i < len(path)-1; i++ {
		if !Can(path[i], path[i+1]) {
			t.Fatalf("expected %s -> %s", path[i], path[i+1])
		}
	}
}

func TestCanRejectsIllegalAndTerminal(t *testing.T) {
	if Can(Created, Executing) {
		t.Fatal("created must not jump to executing")
	}
	if Can(Succeeded, Planning) {
		t.Fatal("succeeded is terminal")
	}
	if Can(Planning, Planning) {
		t.Fatal("self transition is not a change")
	}
	if !Can(Executing, PausedBudget) {
		t.Fatal("budget pause is an escape from executing")
	}
	if !Can(AwaitingApproval, Cancelled) {
		t.Fatal("approval can be cancelled")
	}
	if !Can(Verifying, Observing) {
		t.Fatal("a failed verification can return to observing")
	}
}
