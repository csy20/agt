package orch_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestAskAndPlanAnswerWithoutVerification(t *testing.T) {
	denied := []byte(`{"steps":[
		{"tool":"search_code","args":{"query":"def charge"},"input_tokens":10,"output_tokens":5},
		{"tool":"read_file_range","args":{"path":"payments/processor.py","start":1,"end":20},"input_tokens":10,"output_tokens":5},
		{"tool":"run_test","args":{},"input_tokens":10,"output_tokens":5},
		{"tool":"apply_patch","args":{"path":"payments/processor.py","old":"self.ledger = []\n","new":"self.ledger = []\n"},"input_tokens":10,"output_tokens":5},
		{"tool":"write_file","args":{"path":"payments/processor.py","content":"nope\n","before_sha256":""},"input_tokens":10,"output_tokens":5},
		{"tool":"delete_file","args":{"path":"payments/processor.py","before_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"input_tokens":10,"output_tokens":5},
		{"tool":"git_push","args":{"remote":"origin","branch":"main","commit":"abc123"},"input_tokens":10,"output_tokens":5},
		{"tool":"pr_open","args":{"branch":"main","title":"no"},"input_tokens":10,"output_tokens":5},
		{"finish":true,"text":"charge appends every idempotency key. The retry test expects one ledger row.","input_tokens":10,"output_tokens":5}
	]}`)
	for _, mode := range []string{"ask", "plan"} {
		t.Run(mode, func(t *testing.T) {
			st := testutil.Open(t)
			eng := testutil.NewEngine(t, st, orch.Hooks{})
			fixture := testutil.Fixture(t)
			original := string(readProcessor(t, fixture))
			created := eng.CreateInteraction(t, "how should the retry be fixed?", fixture, mode, 100, 16, denied)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := eng.Orch.Run(ctx, created.ID); err != nil {
				testutil.Dump(t, st, created.ID)
				t.Fatal(err)
			}
			current, err := st.GetRun(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State != run.Succeeded || current.Interaction != mode {
				testutil.Dump(t, st, created.ID)
				t.Fatalf("state %s interaction %s err %s", current.State, current.Interaction, current.Error)
			}
			var result struct {
				Outcome string `json:"outcome"`
				Summary string `json:"summary"`
				Tests   struct {
					Invoked bool `json:"invoked"`
					Passed  bool `json:"passed"`
				} `json:"tests"`
			}
			if err := json.Unmarshal(current.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.Outcome != "answered" || result.Tests.Invoked || result.Tests.Passed || !strings.Contains(result.Summary, "one ledger row") {
				testutil.Dump(t, st, created.ID)
				t.Fatalf("result %+v", result)
			}
			assertNoRepairVerification(t, st, created.ID)
			if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
				t.Fatalf("sandbox execs %d", eng.Runner.ExecCount())
			}
			for _, call := range eng.Calls() {
				switch call {
				case "search_code", "read_file_range", "git_diff":
				default:
					t.Fatalf("executed %s", call)
				}
			}
			deniedTools := map[string]bool{}
			cmds, err := st.ListCommands(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, cmd := range cmds {
				switch cmd.Tool {
				case "run_test", "apply_patch", "write_file", "delete_file", "git_push", "pr_open":
					deniedTools[cmd.Tool] = true
					if cmd.State != "denied" {
						t.Fatalf("%s state %s", cmd.Tool, cmd.State)
					}
				}
			}
			for _, tool := range []string{"run_test", "apply_patch", "write_file", "delete_file", "git_push", "pr_open"} {
				if !deniedTools[tool] {
					t.Fatalf("missing denied %s", tool)
				}
			}
			if string(readProcessor(t, fixture)) != original {
				t.Fatal("source fixture was modified")
			}
		})
	}

	t.Run("plan text only", func(t *testing.T) {
		st := testutil.Open(t)
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		fixture := testutil.Fixture(t)
		script := []byte(`{"steps":[{"finish":true,"text":"Name the failing assertion, then patch charge. Do not edit the test.","input_tokens":10,"output_tokens":5}]}`)
		created := eng.CreateInteraction(t, "plan the retry fix", fixture, "plan", 100, 16, script)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := eng.Orch.Run(ctx, created.ID); err != nil {
			testutil.Dump(t, st, created.ID)
			t.Fatal(err)
		}
		current, err := st.GetRun(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State != run.Succeeded {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s err %s", current.State, current.Error)
		}
		var result struct {
			Outcome string `json:"outcome"`
			Tests   struct {
				Invoked bool `json:"invoked"`
			} `json:"tests"`
		}
		if err := json.Unmarshal(current.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Outcome != "answered" || result.Tests.Invoked {
			t.Fatalf("result %+v", result)
		}
		cmds, err := st.ListCommands(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(cmds) != 0 {
			t.Fatalf("commands %+v", cmds)
		}
		assertNoRepairVerification(t, st, created.ID)
		if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
			t.Fatalf("sandbox execs %d", eng.Runner.ExecCount())
		}
	})
}

func assertNoRepairVerification(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	evs, err := st.EventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		switch ev.Type {
		case "baseline.recorded", "verification.failed", "verification.passed":
			t.Fatalf("repair verification event %s", ev.Type)
		}
	}
}
