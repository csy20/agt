package orch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/testutil"
)

func TestReconnectAfterApprovalDoesNotRepeatTheEdit(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	originalTest := readTest(t, fixture)
	script := []byte(`{"steps":[{"tool":"apply_patch","args":{"path":"tests/test_retry.py","old":"        self.assertEqual(len(processor.ledger), 1)\n","new":"        self.assertEqual(len(processor.ledger), 1)  # reviewed once\n"},"input_tokens":20,"output_tokens":10}]}`)
	created := eng.Create(t, "edit the assertion", fixture, 100, 16, script)
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
	if current.State != run.AwaitingApproval {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s", current.State)
	}
	before, err := st.ListCommands(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstEvents, err := st.EventsAfter(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondEvents, err := st.EventsAfter(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondEvents) != len(firstEvents) {
		t.Fatalf("reread events %d want %d", len(secondEvents), len(firstEvents))
	}
	again, err := st.ListCommands(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(before) || len(before) != 1 || before[0].State != "awaiting_approval" {
		t.Fatalf("commands before approval %+v", before)
	}
	if string(readTest(t, fixture)) != string(originalTest) {
		t.Fatal("reading the event log changed the fixture")
	}
	approvePending(t, st, created.ID)
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	cmds, err := st.ListCommands(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	patches := 0
	for _, cmd := range cmds {
		if cmd.Tool == "apply_patch" {
			patches++
			if cmd.State != "succeeded" {
				t.Fatalf("patch state %s", cmd.State)
			}
		}
	}
	if patches != 1 {
		t.Fatalf("apply_patch commands %d", patches)
	}
	current, err = st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Worktree == "" {
		t.Fatal("missing worktree")
	}
	edited := string(readTest(t, current.Worktree))
	if strings.Count(edited, "reviewed once") != 1 {
		t.Fatalf("worktree edit count in %s", edited)
	}
	if string(readTest(t, fixture)) != string(originalTest) {
		t.Fatal("source fixture was modified")
	}
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	cmds, err = st.ListCommands(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	patches = 0
	for _, cmd := range cmds {
		if cmd.Tool == "apply_patch" {
			patches++
		}
	}
	if patches != 1 {
		t.Fatalf("reconnect created %d patches", patches)
	}
}

func readTest(t *testing.T, root string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "tests", "test_retry.py"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
