package tui

import (
	"strings"
	"testing"

	"github.com/csy20/agt/internal/client"
)

func TestSplitContractArgAndDemoFeatureStayLocal(t *testing.T) {
	path, goal, err := SplitContractArg("contract.json make greet return hello")
	if err != nil || path != "contract.json" || goal != "make greet return hello" {
		t.Fatalf("split %q %q %v", path, goal, err)
	}
	path, goal, err = SplitContractArg("contract.json\nmake greet return hello\n")
	if err != nil || path != "contract.json" || goal != "make greet return hello" {
		t.Fatalf("multiline %q %q %v", path, goal, err)
	}
	if _, _, err := SplitContractArg("contract.json"); err == nil {
		t.Fatal("path without a goal was accepted")
	}

	for _, name := range []string{"feature", "refactor"} {
		m := newModel(Options{Demo: true})
		next, cmd := m.slash(name, "/no/such/contract.json make greet return hello")
		if cmd != nil {
			t.Fatalf("%s demo started a command", name)
		}
		got := next.(model)
		if !strings.Contains(got.snap.Notice, "was not sent") || got.errText != "" {
			t.Fatalf("%s demo notice %q err %q", name, got.snap.Notice, got.errText)
		}
	}

	frame := Render(Snapshot{Width: 80, Height: 24})
	if !strings.Contains(frame, "Repair a failing test") || !strings.Contains(frame, "/feature and /refactor need a contract") {
		t.Fatalf("empty hint %s", frame)
	}
	if !strings.Contains(commandHelp(), "/feature FILE goal") || !strings.Contains(commandHelp(), "/refactor FILE goal") {
		t.Fatal("help does not name the contract commands")
	}
}

func TestVerificationEventsUseTheExistingLayout(t *testing.T) {
	s := Snapshot{Width: 80, Height: 24}
	s = Apply(s, client.Event{Seq: 1, Type: "run.created", Data: []byte(`{"task_type":"feature"}`)})
	s = Apply(s, client.Event{Seq: 2, Type: "contract.baseline", Data: []byte(`{"outcome":"regression_baseline_failed","summary":"acceptance checks passed and a regression check failed","checks":["regression broke fail exit 1"],"failures":["regression broke fail exit 1"]}`)})
	frame := Render(s)
	for _, want := range []string{"task feature", "verification regression_baseline_failed", "check regression broke fail exit 1", "failure regression broke fail exit 1"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q in %s", want, frame)
		}
	}
	s = Apply(s, client.Event{Seq: 3, Type: "verification.recorded", Data: []byte(`{"outcome":"feature_implemented","summary":"acceptance failed before the edit and passed after it","passed":true,"failures":[]}`)})
	if s.Verification != "feature_implemented" || len(s.Failures) != 0 || len(s.Checks) != 1 {
		t.Fatalf("pass wiped checks or kept failures: %+v", s)
	}
	frame = Render(s)
	if !strings.Contains(frame, "verification feature_implemented") || strings.Contains(frame, "failure ") {
		t.Fatalf("pass frame %s", frame)
	}
}
