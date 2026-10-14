package orch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestHappyPathFixesRetry(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	if strings.Contains(string(original), "self.charged") {
		t.Fatal("fixture already contains the fix")
	}
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, testutil.FixScript(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current, err := st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.Succeeded {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	if current.AgentVersion == "" {
		t.Fatal("agent_version was not stamped")
	}
	patched := readProcessor(t, current.Worktree)
	if strings.Count(string(patched), "self.charged = {}") != 1 {
		t.Fatalf("worktree fix count %d", strings.Count(string(patched), "self.charged = {}"))
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	var result struct {
		Diff  string `json:"diff"`
		Tests struct {
			Passed bool `json:"passed"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(current.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Tests.Passed {
		testutil.Dump(t, st, created.ID)
		t.Fatal("verifier did not pass")
	}
	if !strings.Contains(result.Diff, "charged") {
		t.Fatalf("diff %s", result.Diff)
	}
	total, err := st.UsageTotal(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if total <= 0 || total != current.SpentMicros {
		t.Fatalf("usage %d spent %d", total, current.SpentMicros)
	}
	assertMonotonic(t, st, created.ID)
	assertNoContainer(t, created.ID)
}

func TestDeniedPatchDoesNotWrite(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	script := []byte(`{"steps":[
		{"tool":"apply_patch","args":{"path":"../pwned.txt","old":"x","new":"y"},"input_tokens":10,"output_tokens":5},
		{"finish":true,"text":"no","input_tokens":10,"output_tokens":5}
	]}`)
	created := eng.Create(t, "do not escape", fixture, 100, 16, script)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, cmd := range cmds {
		if cmd.Tool != "apply_patch" {
			continue
		}
		denied = true
		if cmd.State != "denied" {
			t.Fatalf("patch state %s", cmd.State)
		}
	}
	if !denied {
		testutil.Dump(t, st, created.ID)
		t.Fatal("missing denied patch command")
	}
	for _, call := range eng.Calls() {
		if call == "apply_patch" {
			t.Fatal("denied patch was executed")
		}
	}
	current, err := st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Worktree != "" {
		pwned := filepath.Join(filepath.Dir(current.Worktree), "pwned.txt")
		if _, err := os.Stat(pwned); err == nil {
			t.Fatalf("wrote %s", pwned)
		}
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	if eng.Runner != nil {
		tests := 0
		for _, call := range eng.Calls() {
			if call == "run_test" {
				tests++
			}
		}
		if eng.Runner.ExecCount() != tests {
			t.Fatalf("sandbox execs %d, run_test calls %d", eng.Runner.ExecCount(), tests)
		}
	}
}

func TestBudgetPauseBeforeSideEffects(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	created := eng.Create(t, "too expensive", fixture, 0, 16, testutil.FixScript(t))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current, err := st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.PausedBudget {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s", current.State)
	}
	if current.Worktree != "" || eng.Created() != 0 {
		t.Fatalf("worktree %q workspaces %d", current.Worktree, eng.Created())
	}
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 0 {
		t.Fatalf("commands recorded: %+v", cmds)
	}
	if eng.Runner != nil && eng.Runner.ExecCount() != 0 {
		t.Fatalf("execs %d", eng.Runner.ExecCount())
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
}

func TestResumeDoesNotDoubleApplyPatch(t *testing.T) {
	st := testutil.Open(t)
	var once atomic.Int32
	eng := testutil.NewEngine(t, st, orch.Hooks{
		BeforeResult: func(_, tool string) error {
			if tool == "apply_patch" && once.CompareAndSwap(0, 1) {
				return orch.ErrInterrupt
			}
			return nil
		},
	})
	fixture := testutil.Fixture(t)
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, testutil.FixScript(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := eng.Orch.Run(ctx, created.ID)
	if !errors.Is(err, orch.ErrInterrupt) {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("first run: %v", err)
	}
	cmd, err := st.OpenCommand(context.Background(), created.ID)
	if err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	if cmd.State != "pending" || cmd.Tool != "apply_patch" {
		t.Fatalf("open command tool %s state %s", cmd.Tool, cmd.State)
	}
	current, err := st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State == run.FailedTerminal {
		testutil.Dump(t, st, created.ID)
		t.Fatal("interrupt failed the run")
	}
	if strings.Count(string(readProcessor(t, current.Worktree)), "self.charged = {}") != 1 {
		t.Fatalf("processor after interrupt:\n%s", readProcessor(t, current.Worktree))
	}
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current, err = st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.Succeeded {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	finished, err := st.GetCommand(context.Background(), created.ID, cmd.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != "succeeded" || !strings.Contains(string(finished.Result), "already applied") {
		t.Fatalf("resumed command %s %s", finished.State, finished.Result)
	}
	if strings.Count(string(readProcessor(t, current.Worktree)), "self.charged = {}") != 1 {
		t.Fatal("patch was applied twice")
	}
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	patches := 0
	for _, item := range cmds {
		if item.Tool == "apply_patch" {
			patches++
		}
	}
	if patches != 1 {
		t.Fatalf("apply_patch commands %d", patches)
	}
	assertNoContainer(t, created.ID)
}

func TestGoAndJavaScriptProfilesRepairDistinctFailures(t *testing.T) {
	cases := []struct {
		name    string
		req     profile.Request
		file    string
		needle  string
		script  string
		timeout time.Duration
	}{
		{
			name:    "gocounter",
			req:     profile.Request{Name: profile.NameGo, Argv: []string{"go", "test", "./..."}, Memory: "512m", TimeoutSec: 180, PIDs: 128},
			file:    "counter.go",
			needle:  "n + 1",
			script:  "fix_gocounter.json",
			timeout: 4 * time.Minute,
		},
		{
			name:    "jsquote",
			req:     profile.Request{Name: profile.NameJavaScript, Argv: []string{"node", "--test"}},
			file:    "quote.js",
			needle:  ".replace(",
			script:  "fix_jsquote.json",
			timeout: 2 * time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.Open(t)
			eng := testutil.NewEngine(t, st, orch.Hooks{})
			fixture := filepath.Join(testutil.RepoRoot(t), "eval", "fixtures", tc.name)
			original, err := os.ReadFile(filepath.Join(fixture, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(original), tc.needle) {
				t.Fatalf("fixture already contains %q", tc.needle)
			}
			script, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "eval", "scripts", tc.script))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(script), tc.needle) && strings.Contains(string(original), tc.needle) {
				t.Fatal("fix is embedded in the fixture")
			}
			prof := eng.MustProfile(t, tc.req)
			if prof.Name != tc.req.Name || prof.Network != "none" || len(prof.Prepare) != 0 {
				t.Fatalf("profile %+v", prof.Identity())
			}
			created := eng.CreateWithProfile(t, "repair the failing test", fixture, 100, 16, script, prof)
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if err := eng.Orch.Run(ctx, created.ID); err != nil {
				testutil.Dump(t, st, created.ID)
				t.Fatal(err)
			}
			current, err := st.GetRun(context.Background(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State != run.Succeeded {
				testutil.Dump(t, st, created.ID)
				t.Fatalf("state %s err %s", current.State, current.Error)
			}
			stored, err := profile.Parse(current.ProfileJSON)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Name != tc.req.Name || strings.Join(stored.Argv, " ") != strings.Join(tc.req.Argv, " ") {
				t.Fatalf("stored profile %+v", stored.Identity())
			}
			patched, err := os.ReadFile(filepath.Join(current.Worktree, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(patched), tc.needle) {
				t.Fatalf("worktree missing %q\n%s", tc.needle, patched)
			}
			after, err := os.ReadFile(filepath.Join(fixture, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(original) {
				t.Fatal("source fixture was modified")
			}
			var result struct {
				Diff  string `json:"diff"`
				Tests struct {
					Passed bool `json:"passed"`
				} `json:"tests"`
			}
			if err := json.Unmarshal(current.Result, &result); err != nil {
				t.Fatal(err)
			}
			if !result.Tests.Passed || !strings.Contains(result.Diff, tc.needle) {
				testutil.Dump(t, st, created.ID)
				t.Fatalf("verify passed=%v diff=%s", result.Tests.Passed, result.Diff)
			}
			if !sawFailingTest(t, st, created.ID) {
				t.Fatal("the profile command did not fail before the repair")
			}
			if eng.Runner != nil && eng.Runner.ExecCount() == 0 {
				t.Fatal("sandbox runner was configured but did not execute")
			}
			assertNoContainer(t, created.ID)
		})
	}
}

func sawFailingTest(t *testing.T, st *store.Store, runID string) bool {
	t.Helper()
	cmds, err := st.ListCommands(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range cmds {
		if cmd.Tool != "run_test" || len(cmd.Result) == 0 {
			continue
		}
		var result struct {
			Exit int `json:"exit_code"`
		}
		if err := json.Unmarshal(cmd.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Exit != 0 {
			return true
		}
	}
	return false
}

func readProcessor(t *testing.T, root string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "payments", "processor.py"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertMonotonic(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	events, err := st.EventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("sequence %d at index %d", ev.Seq, i)
		}
	}
}

func assertNoContainer(t *testing.T, runID string) {
	t.Helper()
	if os.Getenv("AGT_SANDBOX_IMAGE") == "" {
		return
	}
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=agt-run="+runID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container still present for %s: %s", runID, out)
	}
}
