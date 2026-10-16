package orch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
	"github.com/csy20/agt/internal/verify"
	"github.com/jackc/pgx/v5"
)

func TestFeatureAndRefactorContracts(t *testing.T) {
	st := testutil.Open(t)
	root := testutil.RepoRoot(t)
	t.Cleanup(func() {
		feature, err := os.ReadFile(filepath.Join(root, "eval", "fixtures", "featuregate", "greet.py"))
		if err != nil || !strings.Contains(string(feature), "return \"hi\"\n") {
			t.Errorf("featuregate fixture changed: %s %v", feature, err)
		}
		refactor, err := os.ReadFile(filepath.Join(root, "eval", "fixtures", "refactorgate", "greet.py"))
		if err != nil || !strings.Contains(string(refactor), "value = \"hello\"\n") {
			t.Errorf("refactorgate fixture changed: %s %v", refactor, err)
		}
		pay, err := os.ReadFile(filepath.Join(root, "eval", "fixtures", "payrepo", "payments", "processor.py"))
		if err != nil || !strings.Contains(string(pay), "def charge") {
			t.Errorf("payrepo fixture changed: %v", err)
		}
	})

	t.Run("feature implemented", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("greet.py", "return \"hi\"\n", "return \"hello\"\n", "edit greet"),
			contractFinish("greet now returns hello."),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.Succeeded || result.Outcome != verify.OutcomeFeatureImplemented || !result.Tests.Passed || !result.Tests.Invoked {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v err %s", current.State, result, current.Error)
		}
		if !strings.Contains(result.Summary, "acceptance failed before the edit") || !strings.Contains(result.Summary, "verifier executed") || strings.Contains(result.Summary, "tests passed") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertEvidence(t, current, "fail", "pass")
		assertArtifact(t, current, result.VerificationDigest)
		if strings.Contains(readGate(t, filepath.Join(repo, "greet.py")), "return \"hello\"") {
			t.Fatal("source repository was modified before apply")
		}
		if !strings.Contains(readGate(t, filepath.Join(current.Worktree, "greet.py")), "return \"hello\"") {
			t.Fatal("worktree does not contain the implementation")
		}
		assertChecks(t, eng, 4, 4)
		if n := modelCalls(t, st, created.ID); n != 2 {
			t.Fatalf("model calls %d", n)
		}
		if !eventText(t, st, created.ID, "greet now returns hello.") {
			t.Fatal("scripted model text was not recorded")
		}
	})

	t.Run("acceptance passes while regression fails", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		contract := contractJSON(t,
			pyCheck("broke", verify.RoleRegression, "tests.test_broken", "tests/test_broken.py"),
			pyCheck("still", verify.RoleAcceptance, "tests.test_regress", "tests/test_regress.py"),
		)
		created, digest := createContractRun(t, eng, "keep the old behavior", repo, verify.TaskFeature, contract, scriptSteps(t, contractFinish("unused")), py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeRegressionBaselineFailed || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if !strings.Contains(result.Summary, "acceptance checks passed") || !strings.Contains(result.Summary, "regression check failed") || strings.Contains(result.Summary, "newly implemented") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertChecks(t, eng, 2, 2)
		if n := modelCalls(t, st, created.ID); n != 0 {
			t.Fatalf("model calls %d", n)
		}
	})

	t.Run("already satisfied", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		writeGate(t, filepath.Join(repo, "greet.py"), "def greet():\n    return \"hello\"\n")
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), scriptSteps(t, contractFinish("tests passed")), py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeAlreadySatisfied || result.Tests.Passed || result.Outcome == verify.OutcomeFeatureImplemented {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if !strings.Contains(result.Summary, "not a newly implemented feature") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertChecks(t, eng, 2, 2)
		if n := modelCalls(t, st, created.ID); n != 0 {
			t.Fatalf("model calls %d", n)
		}
		if strings.Contains(readGate(t, filepath.Join(repo, "greet.py")), "return \"hi\"") {
			t.Fatal("already-satisfied copy lost the passing implementation")
		}
	})

	t.Run("import error is infrastructure", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		contract := contractJSON(t,
			pyCheck("reg", verify.RoleRegression, "tests.test_regress", "tests/test_regress.py"),
			checkSpec{Name: "missing", Role: verify.RoleAcceptance, Argv: []string{"python3", "-m", "unittest", "tests.missing_module"}, Paths: []string{"tests/test_accept.py"}},
		)
		created, _ := createContractRun(t, eng, "missing module", repo, verify.TaskFeature, contract, scriptSteps(t, contractFinish("unused")), py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		assertInfra(t, st, eng, current, 2)
	})

	t.Run("empty test run is infrastructure", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		contract := contractJSON(t,
			pyCheck("reg", verify.RoleRegression, "tests.test_regress", "tests/test_regress.py"),
			checkSpec{Name: "none", Role: verify.RoleAcceptance, Argv: []string{"python3", "-m", "unittest", "discover", "-s", "tests", "-p", "zzz_none.py"}, Paths: []string{"tests/test_accept.py"}},
		)
		created, _ := createContractRun(t, eng, "empty acceptance", repo, verify.TaskFeature, contract, scriptSteps(t, contractFinish("unused")), py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		assertInfra(t, st, eng, current, 2)
		if !strings.Contains(string(current.BaselineJSON), "empty test run") {
			t.Fatalf("baseline %s", current.BaselineJSON)
		}
	})

	t.Run("printed pass is not a real acceptance failure", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		contract := contractJSON(t,
			pyCheck("reg", verify.RoleRegression, "tests.test_regress", "tests/test_regress.py"),
			checkSpec{Name: "printed", Role: verify.RoleAcceptance, Argv: []string{"python3", "-c", "print(\"tests passed\")"}, Paths: []string{"tests/test_accept.py"}},
		)
		created, _ := createContractRun(t, eng, "printed pass", repo, verify.TaskFeature, contract, scriptSteps(t, contractFinish("unused")), py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		assertInfra(t, st, eng, current, 2)
	})

	t.Run("model claim does not verify a bad edit", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("greet.py", "return \"hi\"\n", "return \"bye\"\n", "tests passed"),
			contractFinish("tests passed"),
			contractFinish("tests passed"),
			contractFinish("tests passed"),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeAcceptanceFailed || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if strings.Contains(result.Summary, "tests passed") || !strings.Contains(result.Summary, "acceptance check failed") {
			t.Fatalf("summary %q", result.Summary)
		}
		if !eventText(t, st, created.ID, "tests passed") {
			t.Fatal("scripted claim was not recorded")
		}
		assertContractLocked(t, current, digest)
		if !strings.Contains(readGate(t, filepath.Join(current.Worktree, "greet.py")), "return \"bye\"") {
			t.Fatal("bad edit was not applied in the worktree")
		}
		if strings.Contains(readGate(t, filepath.Join(repo, "greet.py")), "return \"bye\"") {
			t.Fatal("source repository was modified")
		}
		assertCheckRange(t, eng, 6, 8)
	})

	t.Run("protected edit is denied and the implementation still verifies", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("tests/test_accept.py", "self.assertEqual(greet(), \"hello\")", "self.assertEqual(greet(), \"hi\")", "weaken"),
			contractPatch("greet.py", "return \"hi\"\n", "return \"hello\"\n", "implement"),
			contractFinish("greet now returns hello."),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.Succeeded || result.Outcome != verify.OutcomeFeatureImplemented {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v err %s", current.State, result, current.Error)
		}
		assertContractLocked(t, current, digest)
		accept := readGate(t, filepath.Join(current.Worktree, "tests", "test_accept.py"))
		if !strings.Contains(accept, "self.assertEqual(greet(), \"hello\")") || strings.Contains(accept, "greet(), \"hi\"") {
			t.Fatal("protected acceptance check was edited")
		}
		denied := false
		cmds, err := st.ListCommands(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range cmds {
			if cmd.Tool == "apply_patch" && cmd.State == "denied" && strings.Contains(string(cmd.Result), "protected") {
				denied = true
			}
		}
		if !denied {
			testutil.Dump(t, st, created.ID)
			t.Fatal("protected edit was not denied")
		}
		assertChecks(t, eng, 4, 4)
	})

	t.Run("weaken only does not implement the feature", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("tests/test_accept.py", "self.assertEqual(greet(), \"hello\")", "self.assertEqual(greet(), \"hi\")", "weaken"),
			contractFinish("done"),
			contractFinish("done"),
			contractFinish("done"),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeAcceptanceFailed || result.Outcome == verify.OutcomeFeatureImplemented {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		assertContractLocked(t, current, digest)
		if strings.Contains(readGate(t, filepath.Join(current.Worktree, "tests", "test_accept.py")), "greet(), \"hi\"") {
			t.Fatal("denied edit changed the acceptance check")
		}
	})

	t.Run("tampering a protected file is a weakened contract", func(t *testing.T) {
		var runID string
		eng := testutil.NewEngine(t, st, orch.Hooks{
			BeforeResult: func(_, tool string) error {
				if tool != "apply_patch" || runID == "" {
					return nil
				}
				current, err := st.GetRun(context.Background(), runID)
				if err != nil {
					return err
				}
				path := filepath.Join(current.Worktree, "tests", "test_accept.py")
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				next := strings.Replace(string(body), "greet(), \"hello\"", "greet(), \"hi\"", 1)
				if next == string(body) {
					return errors.New("acceptance assertion was not found")
				}
				return os.WriteFile(path, []byte(next), 0o644)
			},
		})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("greet.py", "return \"hi\"\n", "return \"hello\"\n", "implement"),
			contractFinish("greet now returns hello."),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		runID = created.ID
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeContractWeakened || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if !strings.Contains(result.Summary, "stored contract was not revised") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		if current.State == run.Succeeded {
			t.Fatal("weakened contract was applied")
		}
	})

	t.Run("test approval does not revise the contract", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		script := scriptSteps(t,
			contractPatch("tests/test_other.py", "self.assertEqual(1, 1)", "self.assertEqual(1, 1)  # reviewed", "review"),
			contractPatch("greet.py", "return \"hi\"\n", "return \"hello\"\n", "implement"),
			contractFinish("greet now returns hello."),
		)
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), script, py("tests.test_regress"))
		current := runTo(t, eng, created.ID)
		if current.State != run.AwaitingApproval {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s err %s", current.State, current.Error)
		}
		cmd, err := st.OpenCommand(context.Background(), created.ID)
		if err != nil || cmd.State != "awaiting_approval" || cmd.Tool != "apply_patch" {
			t.Fatalf("command %+v err %v", cmd, err)
		}
		before := readGate(t, filepath.Join(current.Worktree, "tests", "test_other.py"))
		_, err = st.Approve(context.Background(), store.Approval{
			ID: id.New("apr_"), RunID: created.ID, CommandID: cmd.CommandID,
			ArgsHash: "deadbeef", Approver: "test", Decision: "approved",
		})
		if !errors.Is(err, store.ErrHashMismatch) {
			t.Fatalf("wrong hash: %v", err)
		}
		if got := readGate(t, filepath.Join(current.Worktree, "tests", "test_other.py")); got != before {
			t.Fatal("wrong hash wrote the test file")
		}
		held, err := st.GetRun(context.Background(), created.ID)
		if err != nil || held.State != run.AwaitingApproval || held.ContractDigest != digest {
			t.Fatalf("held %+v err %v", held.State, err)
		}
		if _, err := st.Approve(context.Background(), store.Approval{
			ID: id.New("apr_"), RunID: created.ID, CommandID: cmd.CommandID,
			ArgsHash: cmd.ArgsHash, Approver: "test", Decision: "approved",
		}); err != nil {
			t.Fatal(err)
		}
		current = runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.Succeeded || result.Outcome != verify.OutcomeFeatureImplemented {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v err %s", current.State, result, current.Error)
		}
		assertContractLocked(t, current, digest)
		if !strings.Contains(readGate(t, filepath.Join(current.Worktree, "tests", "test_other.py")), "# reviewed") {
			t.Fatal("approved edit was not applied in the worktree")
		}
		if strings.Contains(readGate(t, filepath.Join(repo, "tests", "test_other.py")), "# reviewed") {
			t.Fatal("approval wrote the source repository")
		}
	})

	t.Run("refactor checks pass without an equivalence claim", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "refactorgate")
		script := scriptSteps(t,
			contractPatch("greet.py", "    value = \"hello\"\n    return value\n", "    word = \"hello\"\n    return word\n", "rename"),
			contractFinish("renamed the local"),
		)
		created, digest := createContractRun(t, eng, "rename the local", repo, verify.TaskRefactor, refactorContract(), script, py("tests.test_protected"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.Succeeded || result.Outcome != verify.OutcomeChecksPassed || !result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v err %s", current.State, result, current.Error)
		}
		if !strings.Contains(result.Summary, "not a proof of behavioral equivalence") || strings.Contains(strings.ToLower(result.Summary), "equivalent") && !strings.Contains(result.Summary, "not a proof") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertArtifact(t, current, result.VerificationDigest)
		if strings.Contains(readGate(t, filepath.Join(repo, "greet.py")), "word = \"hello\"") {
			t.Fatal("source repository was modified")
		}
		if !strings.Contains(readGate(t, filepath.Join(current.Worktree, "greet.py")), "word = \"hello\"") {
			t.Fatal("worktree was not refactored")
		}
		assertChecks(t, eng, 2, 2)
	})

	t.Run("refactor baseline failure", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		contract := contractJSON(t, pyCheck("broke", verify.RoleProtected, "tests.test_broken", "tests/test_broken.py"))
		created, digest := createContractRun(t, eng, "rename something", repo, verify.TaskRefactor, contract, scriptSteps(t, contractFinish("unused")), py("tests.test_broken"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeRefactorBaselineFailed || result.Outcome == verify.OutcomeChecksPassed || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if !strings.Contains(result.Summary, "not a verified refactor") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertChecks(t, eng, 1, 1)
		if n := modelCalls(t, st, created.ID); n != 0 {
			t.Fatalf("model calls %d", n)
		}
	})

	t.Run("refactor final check fails", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "refactorgate")
		script := scriptSteps(t,
			contractPatch("greet.py", "    value = \"hello\"\n    return value\n", "    return \"bye\"\n", "change behavior"),
			contractFinish("done"),
			contractFinish("done"),
			contractFinish("done"),
		)
		created, digest := createContractRun(t, eng, "rename the local", repo, verify.TaskRefactor, refactorContract(), script, py("tests.test_protected"))
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeChecksFailed || result.Outcome == verify.OutcomeChecksPassed || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if !strings.Contains(result.Summary, "not a verified refactor") {
			t.Fatalf("summary %q", result.Summary)
		}
		assertContractLocked(t, current, digest)
		assertCheckRange(t, eng, 3, 4)
	})

	t.Run("altered contract is not authoritative", func(t *testing.T) {
		eng := testutil.NewEngine(t, st, orch.Hooks{})
		repo := copyGate(t, "featuregate")
		created, digest := createContractRun(t, eng, "make greet return hello", repo, verify.TaskFeature, featureContract(), scriptSteps(t, contractFinish("unused")), py("tests.test_regress"))
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, testutil.URL(st))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		weaker := contractJSON(t, pyCheck("other", verify.RoleAcceptance, "tests.test_other", "tests/test_other.py"))
		if _, err := conn.Exec(ctx, `UPDATE runs SET contract_json=$2::jsonb WHERE id=$1`, created.ID, string(weaker)); err != nil {
			t.Fatal(err)
		}
		current := runTo(t, eng, created.ID)
		result := mustResult(t, current)
		if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeVerificationInfra || result.Tests.Passed {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("state %s result %+v", current.State, result)
		}
		if current.ContractDigest != digest || !strings.Contains(result.Summary, "digest") {
			t.Fatalf("digest %s summary %q", current.ContractDigest, result.Summary)
		}
		if n := modelCalls(t, st, created.ID); n != 0 {
			t.Fatalf("model calls %d", n)
		}
		assertChecks(t, eng, 0, 0)
	})
}

func TestAskCannotOwnAFeatureContract(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	repo := copyGate(t, "featuregate")
	prof := eng.MustProfile(t, profile.Request{Name: profile.NamePython, Argv: py("tests.test_regress")})
	_, canonical, digest, err := verify.Parse(featureContract(), verify.TaskFeature, prof.Name)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ask", "plan"} {
		_, _, err := st.CreateRun(context.Background(), store.CreateInput{
			Goal: "explain greet", RepoPath: repo, BudgetCents: 100, MaxSteps: 4, WallSeconds: 300,
			Provider: "fake", RequestHash: id.New("req_"), ScriptJSON: scriptSteps(t, contractFinish("unused")),
			ProfileJSON: raw, Interaction: mode, TaskType: verify.TaskFeature,
			ContractJSON: canonical, ContractDigest: digest,
		})
		if err == nil || !strings.Contains(err.Error(), "agent") {
			t.Fatalf("%s feature stored: %v", mode, err)
		}
	}
	_, _, err = st.CreateRun(context.Background(), store.CreateInput{
		Goal: "repair", RepoPath: repo, BudgetCents: 100, MaxSteps: 4, WallSeconds: 300,
		Provider: "fake", RequestHash: id.New("req_"), ScriptJSON: scriptSteps(t, contractFinish("unused")),
		ProfileJSON: raw, Interaction: "agent", TaskType: verify.TaskRepair,
		ContractJSON: canonical, ContractDigest: digest,
	})
	if err == nil || !strings.Contains(err.Error(), "repair") {
		t.Fatalf("repair contract stored: %v", err)
	}
}

func assertInfra(t *testing.T, st *store.Store, eng *testutil.Engine, current store.Run, checks int) {
	t.Helper()
	result := mustResult(t, current)
	if current.State != run.FailedTerminal || result.Outcome != verify.OutcomeVerificationInfra || result.Tests.Passed || result.Outcome == verify.OutcomeAlreadySatisfied || result.Outcome == verify.OutcomeAcceptanceFailed {
		testutil.Dump(t, st, current.ID)
		t.Fatalf("state %s result %+v", current.State, result)
	}
	if !strings.Contains(result.Summary, "not executed") {
		t.Fatalf("summary %q", result.Summary)
	}
	assertChecks(t, eng, checks, checks)
	if n := modelCalls(t, st, current.ID); n != 0 {
		t.Fatalf("model calls %d", n)
	}
}

func assertEvidence(t *testing.T, current store.Run, before, after string) {
	t.Helper()
	base := decodeEvidence(t, current.BaselineJSON)
	final := decodeEvidence(t, current.FinalJSON)
	if base.Phase != "baseline" || final.Phase != "final" {
		t.Fatalf("phases %s %s", base.Phase, final.Phase)
	}
	if kind := roleKind(base.Checks, verify.RoleAcceptance); kind != before {
		t.Fatalf("baseline acceptance %s", kind)
	}
	if kind := roleKind(base.Checks, verify.RoleRegression); kind != "pass" {
		t.Fatalf("baseline regression %s", kind)
	}
	if kind := roleKind(final.Checks, verify.RoleAcceptance); kind != after {
		t.Fatalf("final acceptance %s", kind)
	}
	if kind := roleKind(final.Checks, verify.RoleRegression); kind != "pass" {
		t.Fatalf("final regression %s", kind)
	}
}

func decodeEvidence(t *testing.T, raw json.RawMessage) struct {
	Phase  string               `json:"phase"`
	Checks []verify.CheckResult `json:"checks"`
} {
	t.Helper()
	var doc struct {
		Phase  string               `json:"phase"`
		Checks []verify.CheckResult `json:"checks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func roleKind(checks []verify.CheckResult, role string) string {
	for _, check := range checks {
		if check.Role == role {
			return check.Kind
		}
	}
	return ""
}

func assertArtifact(t *testing.T, current store.Run, want string) {
	t.Helper()
	art, err := patch.Build(orch.BasePath(current.Worktree), current.Worktree, patch.Meta{
		RunID: current.ID, RepoPath: current.RepoPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if art.Digest == "" || art.Digest != current.VerificationDigest || art.Digest != want {
		t.Fatalf("artifact %s column %s result %s", art.Digest, current.VerificationDigest, want)
	}
}

func assertContractLocked(t *testing.T, current store.Run, digest string) {
	t.Helper()
	if current.ContractDigest != digest {
		t.Fatalf("contract digest changed %s -> %s", digest, current.ContractDigest)
	}
	loaded, err := verify.Load(current.ContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if verify.Digest(canonical) != digest {
		t.Fatal("stored contract no longer matches its digest")
	}
}

func assertChecks(t *testing.T, eng *testutil.Engine, want, execs int) {
	t.Helper()
	got := countName(eng.Calls(), "check")
	if got != want {
		t.Fatalf("checks %d want %d calls %v", got, want, eng.Calls())
	}
	if countName(eng.Calls(), "run_test") != 0 {
		t.Fatalf("model run_test executed: %v", eng.Calls())
	}
	// A nil runner means HostExec ran the profile argv. That is not container support.
	if eng.Runner != nil && eng.Runner.ExecCount() != execs {
		t.Fatalf("sandbox execs %d want %d", eng.Runner.ExecCount(), execs)
	}
}

func assertCheckRange(t *testing.T, eng *testutil.Engine, min, max int) {
	t.Helper()
	got := countName(eng.Calls(), "check")
	if got < min || got > max {
		t.Fatalf("checks %d want %d..%d calls %v", got, min, max, eng.Calls())
	}
	if countName(eng.Calls(), "run_test") != 0 {
		t.Fatalf("model run_test executed: %v", eng.Calls())
	}
	if eng.Runner != nil && eng.Runner.ExecCount() != got {
		t.Fatalf("sandbox execs %d checks %d", eng.Runner.ExecCount(), got)
	}
}

func countName(calls []string, name string) int {
	n := 0
	for _, call := range calls {
		if call == name {
			n++
		}
	}
	return n
}

func modelCalls(t *testing.T, st *store.Store, runID string) int {
	t.Helper()
	n, err := st.CountEvents(context.Background(), runID, "model.called")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func eventText(t *testing.T, st *store.Store, runID, needle string) bool {
	t.Helper()
	evs, err := st.EventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == "model.called" && strings.Contains(string(ev.Payload), needle) {
			return true
		}
	}
	return false
}

type contractResult struct {
	Outcome            string `json:"outcome"`
	Summary            string `json:"summary"`
	TaskType           string `json:"task_type"`
	ContractDigest     string `json:"contract_digest"`
	VerificationDigest string `json:"verification_digest"`
	Tests              struct {
		Passed  bool `json:"passed"`
		Invoked bool `json:"invoked"`
	} `json:"tests"`
}

func mustResult(t *testing.T, current store.Run) contractResult {
	t.Helper()
	var result contractResult
	if err := json.Unmarshal(current.Result, &result); err != nil {
		t.Fatalf("result %s: %v", current.Result, err)
	}
	return result
}

func runTo(t *testing.T, eng *testutil.Engine, runID string) store.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, runID); err != nil {
		testutil.Dump(t, eng.Store, runID)
		t.Fatal(err)
	}
	current, err := eng.Store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func createContractRun(t *testing.T, eng *testutil.Engine, goal, repo, task string, contract, script []byte, argv []string) (store.Run, string) {
	t.Helper()
	prof := eng.MustProfile(t, profile.Request{Name: profile.NamePython, Argv: argv})
	_, canonical, digest, err := verify.Parse(contract, task, prof.Name)
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := eng.Store.CreateRun(context.Background(), store.CreateInput{
		Goal: goal, RepoPath: repo, BudgetCents: 100, MaxSteps: 16, WallSeconds: 300,
		Provider: "fake", RequestHash: id.New("req_"), ScriptJSON: script, ProfileJSON: raw,
		Interaction: "agent", TaskType: task, ContractJSON: canonical, ContractDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created, digest
}

type checkSpec struct {
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	Argv  []string `json:"argv"`
	Paths []string `json:"paths"`
}

func featureContract() []byte {
	return contractJSON(nil,
		pyCheck("reg", verify.RoleRegression, "tests.test_regress", "tests/test_regress.py"),
		pyCheck("accept", verify.RoleAcceptance, "tests.test_accept", "tests/test_accept.py"),
	)
}

func refactorContract() []byte {
	return contractJSON(nil, pyCheck("kept", verify.RoleProtected, "tests.test_protected", "tests/test_protected.py"))
}

func pyCheck(name, role, module, path string) checkSpec {
	return checkSpec{Name: name, Role: role, Argv: py(module), Paths: []string{path}}
}

func py(module string) []string {
	return []string{"python3", "-m", "unittest", module}
}

func contractJSON(t *testing.T, checks ...checkSpec) []byte {
	raw, err := json.Marshal(map[string]any{"version": verify.ContractVersion, "checks": checks})
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return raw
}

func scriptSteps(t *testing.T, steps ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"steps": steps})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func contractPatch(path, old, new, text string) map[string]any {
	return map[string]any{
		"tool": "apply_patch", "text": text, "input_tokens": 10, "output_tokens": 5,
		"args": map[string]string{"path": path, "old": old, "new": new},
	}
}

func contractFinish(text string) map[string]any {
	return map[string]any{"finish": true, "text": text, "input_tokens": 10, "output_tokens": 5}
}

func copyGate(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(testutil.RepoRoot(t), "eval", "fixtures", name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func readGate(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func writeGate(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
