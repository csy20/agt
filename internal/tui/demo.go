package tui

import (
	"fmt"
	"strings"

	"github.com/csy20/agt/internal/client"
)

const (
	exampleHash   = "9f2ac0c1e4b87a6d5c0e1f2233445566778899aabbccddeeff00112233445566"
	exampleDigest = "c0ffee00a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c"
	exampleDiff   = `--- a/payments/processor.py
+++ b/payments/processor.py
@@ -5,6 +5,10 @@ class PaymentProcessor:
     def __init__(self):
         self.ledger = []
+        self.charged = {}
 
     def charge(self, idempotency_key: str, amount: int) -> int:
-        self.ledger.append((idempotency_key, amount))
-        return len(self.ledger)
+        if idempotency_key in self.charged:
+            return self.charged[idempotency_key]
+        self.ledger.append((idempotency_key, amount))
+        self.charged[idempotency_key] = len(self.ledger)
+        return self.charged[idempotency_key]
`
)

// Frame is one named example screen.
type Frame struct {
	Name     string
	Snapshot Snapshot
}

// ExampleFrames are the interactive concept, drawn from example events.
// They are not a live repair and they do not change a repository.
func ExampleFrames(width int, color bool) []Frame {
	if width <= 0 {
		width = 96
	}
	base := func(scene string) Snapshot {
		return Snapshot{
			Width: width, Height: 32, Color: color, Example: true, Scene: scene,
			Repo: "eval/fixtures/payrepo", Model: "local-repair", Mode: "agent",
			Profile: "python", Network: "none", BudgetCents: 100,
			Placeholder: "Repair a failing test, or /help",
			RunID:       "run_example",
		}
	}
	composer := base("composer")
	composer.RunState = "idle"

	stream := base("stream")
	stream.RunState = "EXECUTING"
	stream.Messages = []Message{{Role: "you", Text: "make the retry test pass"}}
	stream = Apply(stream, delta("charge appends every idempotency key. "))
	stream = Apply(stream, delta("I'll read the retry assertion before editing."))
	stream = Apply(stream, finished("search_code", 0, "payments/processor.py:8"))
	stream.Tools[0].Detail = `query "def charge"`

	approval := base("approval")
	approval.RunState = "AWAITING_APPROVAL"
	approval.SpentMicros = 2640
	approval.Messages = []Message{
		{Role: "you", Text: "make the retry test pass"},
		{Role: "agt", Text: "The assertion is len(processor.ledger) == 1. Editing tests/test_retry.py needs approval."},
	}
	approval.Tools = []ToolLine{
		{Name: "search_code", Detail: `query "def charge"`, Exit: "0", State: "finished"},
		{Name: "run_test", Exit: "1", Output: "FAIL: tests.test_retry", State: "finished", Expanded: true},
		{Name: "apply_patch", Detail: "awaiting approval", State: "approval"},
	}
	approval.Approval = &Approval{
		Tool: "apply_patch", CommandID: "cmd_example", ArgsHash: exampleHash,
		Reason: "edits that change test files require approval", Path: "tests/test_retry.py",
	}

	patch := base("patch")
	patch.RunState = "SUCCEEDED"
	patch.SpentMicros = 2640
	patch.Messages = []Message{
		{Role: "you", Text: "make the retry test pass"},
		{Role: "agt", Text: "charge now returns the stored result for a repeated idempotency key. tests/test_retry.py was not edited."},
	}
	patch.Diff = exampleDiff
	patch.Digest = exampleDigest
	patch.View = "diff"

	plan := base("plan")
	plan.Mode = "plan"
	plan.RunState = "OBSERVING"
	plan.Messages = []Message{
		{Role: "you", Text: "/plan how should the retry be fixed?"},
		{Role: "agt", Text: "charge(self, idempotency_key, amount) appends every call. test_retry_does_not_double_charge expects one ledger row. Patch payments/processor.py. Do not edit the test."},
	}
	plan.Tools = []ToolLine{{
		Name: "run_test", State: "denied", Detail: "denied: ask and plan modes are read-only",
	}}

	narrow := approval
	narrow.Scene = "narrow"
	narrow.Width = 48
	narrow.Height = 28
	narrow.Messages = append([]Message(nil), approval.Messages...)
	narrow.Tools = append([]ToolLine(nil), approval.Tools...)
	if approval.Approval != nil {
		copyApproval := *approval.Approval
		narrow.Approval = &copyApproval
	}

	return []Frame{
		{Name: "composer", Snapshot: composer},
		{Name: "stream", Snapshot: stream},
		{Name: "approval", Snapshot: approval},
		{Name: "patch", Snapshot: patch},
		{Name: "plan", Snapshot: plan},
		{Name: "narrow", Snapshot: narrow},
	}
}

// Dump renders the example frames as plain text unless color is set.
func Dump(color bool) string {
	frames := ExampleFrames(104, color)
	var b strings.Builder
	for _, frame := range frames {
		fmt.Fprintf(&b, "--- %s ---\n%s\n\n", frame.Name, Render(frame.Snapshot))
	}
	return b.String()
}

func delta(text string) client.Event {
	return client.Event{Type: "assistant.delta", Data: []byte(fmt.Sprintf(`{"text":%q}`, text))}
}

func finished(tool string, exit int, output string) client.Event {
	return client.Event{Type: "command.finished", Data: []byte(fmt.Sprintf(
		`{"tool":%q,"exit_code":%d,"output_excerpt":%q}`, tool, exit, output))}
}
