package orch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

// scriptedTask is one fake-provider repair. The hidden check is not a file in
// the repository the agent can edit. This batch is not a hosted-model evaluation.
type scriptedTask struct {
	Name    string
	Lang    string
	Kind    string
	K       int
	Visible int
}

func scriptedTasks() []scriptedTask {
	var out []scriptedTask
	for i := 1; i <= 6; i++ {
		out = append(out, scriptedTask{Name: fmt.Sprintf("py-repair-%d", i), Lang: "python", Kind: "repair", K: i, Visible: 10 + i})
	}
	out = append(out,
		scriptedTask{Name: "py-recovery", Lang: "python", Kind: "recovery", K: 7, Visible: 20},
		scriptedTask{Name: "py-false-success", Lang: "python", Kind: "false_success", K: 8, Visible: 21},
		scriptedTask{Name: "py-honest-failure", Lang: "python", Kind: "honest_failure", K: 9, Visible: 22},
	)
	for i := 1; i <= 5; i++ {
		out = append(out, scriptedTask{Name: fmt.Sprintf("js-repair-%d", i), Lang: "javascript", Kind: "repair", K: i, Visible: 30 + i})
	}
	out = append(out,
		scriptedTask{Name: "js-recovery", Lang: "javascript", Kind: "recovery", K: 6, Visible: 40},
		scriptedTask{Name: "js-false-success", Lang: "javascript", Kind: "false_success", K: 7, Visible: 41},
	)
	for i := 1; i <= 3; i++ {
		out = append(out, scriptedTask{Name: fmt.Sprintf("go-repair-%d", i), Lang: "go", Kind: "repair", K: i, Visible: 50 + i})
	}
	out = append(out, scriptedTask{Name: "go-honest-failure", Lang: "go", Kind: "honest_failure", K: 4, Visible: 60})
	return out
}

func TestScriptedTaskList(t *testing.T) {
	tasks := scriptedTasks()
	if len(tasks) < 20 {
		t.Fatalf("tasks %d", len(tasks))
	}
	langs := map[string]int{}
	kinds := map[string]int{}
	seen := map[string]bool{}
	for _, task := range tasks {
		if seen[task.Name] || task.K == 0 || task.Visible == 0 {
			t.Fatalf("bad task %+v", task)
		}
		seen[task.Name] = true
		langs[task.Lang]++
		kinds[task.Kind]++
	}
	if langs["python"] == 0 || langs["go"] == 0 || langs["javascript"] == 0 {
		t.Fatalf("languages %v", langs)
	}
	if kinds["false_success"] < 1 || kinds["honest_failure"] < 1 || kinds["recovery"] < 1 || kinds["repair"] < 1 {
		t.Fatalf("kinds %v", kinds)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := writeTaskRepo(tasks[0], repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "hidden_check.py")); !os.IsNotExist(err) {
		t.Fatal("hidden checker was written into the repository")
	}
}

func TestScriptedRepairBatch(t *testing.T) {
	tasks := scriptedTasks()
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	runner := "host"
	if eng.Runner != nil {
		runner = "sandbox"
	}
	if os.Getenv("AGT_INTEGRATION") == "1" && runner != "sandbox" {
		t.Fatal("AGT_INTEGRATION=1 ran the batch on the host runner")
	}

	var solved, falseSuccess, honest, recovery int
	var cost int64
	var latency []time.Duration
	byLang := map[string]int{}

	for _, task := range tasks {
		byLang[task.Lang]++
		repo := filepath.Join(t.TempDir(), "repo")
		if err := writeTaskRepo(task, repo); err != nil {
			t.Fatal(err)
		}
		before := snapshotTree(t, repo)
		prof := eng.MustProfile(t, task.request())
		script := mustScript(t, task.steps())
		created := eng.CreateWithProfile(t, "make the visible test pass", repo, 100, 16, script, prof)
		start := time.Now()
		current := driveTask(t, eng, st, created.ID, task)
		elapsed := time.Since(start)
		latency = append(latency, elapsed)
		if got := snapshotTree(t, repo); !sameTree(before, got) {
			t.Fatalf("%s modified the source repository", task.Name)
		}
		result := decodeRepair(t, current.Result)
		hiddenOK := hiddenPasses(t, task, current.Worktree)
		class := classifyTask(t, st, task, current, result, hiddenOK)
		switch class {
		case "solved":
			solved++
			if task.Kind == "recovery" {
				recovery++
			}
		case "false_success":
			falseSuccess++
		case "honest_failure":
			honest++
		default:
			t.Fatalf("%s class %s", task.Name, class)
		}
		total, err := st.UsageTotal(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if total <= 0 {
			testutil.Dump(t, st, created.ID)
			t.Fatalf("%s recorded no cost", task.Name)
		}
		cost += total
		assertNoContainer(t, created.ID)
		t.Logf("eval %s %s %s %s cost=%d latency=%s hidden=%v", task.Name, task.Lang, task.Kind, result.Outcome, total, elapsed.Round(time.Millisecond), hiddenOK)
	}

	if solved+falseSuccess+honest != len(tasks) {
		t.Fatalf("classes solved=%d false=%d honest=%d tasks=%d", solved, falseSuccess, honest, len(tasks))
	}
	if solved != 16 || falseSuccess != 2 || honest != 2 || recovery != 2 {
		t.Fatalf("want 16 solved, 2 false successes, 2 honest failures, 2 recoveries; got %d %d %d %d", solved, falseSuccess, honest, recovery)
	}
	if eng.Runner != nil && eng.Runner.ExecCount() == 0 {
		t.Fatal("sandbox runner was configured but did not execute")
	}
	minL, maxL, totalL := latencyStats(latency)
	t.Logf("EVAL tasks=%d solved=%d solve_rate=%d/%d false_success=%d false_success_rate=%d/%d honest_failure=%d recovery=%d cost_micros=%d latency_min=%s latency_max=%s latency_total=%s runner=%s hidden_check=host_process_outside_worktree provider=fake languages=python:%d,javascript:%d,go:%d",
		len(tasks), solved, solved, len(tasks), falseSuccess, falseSuccess, len(tasks), honest, recovery, cost,
		minL.Round(time.Millisecond), maxL.Round(time.Millisecond), totalL.Round(time.Millisecond),
		runner, byLang["python"], byLang["javascript"], byLang["go"])
}

func (task scriptedTask) request() profile.Request {
	switch task.Lang {
	case "python":
		return profile.Request{Name: profile.NamePython, Argv: []string{"python", "-m", "unittest", "tests.test_calc"}}
	case "go":
		return profile.Request{Name: profile.NameGo, Argv: []string{"go", "test", "./..."}, Memory: "512m", TimeoutSec: 120, PIDs: 128}
	default:
		return profile.Request{Name: profile.NameJavaScript, Argv: []string{"node", "--test"}}
	}
}

func (task scriptedTask) steps() []scriptStep {
	impl := task.implPath()
	buggy := buggyLine(task.Lang, task.K)
	fixed := fixedLine(task.Lang, task.K)
	comment := commentLine(task.Lang, task.K)
	switch task.Kind {
	case "repair":
		return []scriptStep{
			runTestStep(),
			patchStep(impl, buggy, fixed),
			finishStep("repaired"),
		}
	case "recovery":
		return []scriptStep{
			runTestStep(),
			patchStep(impl, buggy, comment),
			finishStep("still wrong"),
			patchStep(impl, comment, fixed),
			finishStep("repaired"),
		}
	case "false_success":
		return []scriptStep{
			runTestStep(),
			patchStep(task.testPath(), visibleAssert(task, task.Visible+task.K), visibleAssert(task, task.Visible-task.K)),
			finishStep("edited the test"),
		}
	default:
		return []scriptStep{
			runTestStep(),
			patchStep(impl, buggy, comment),
			finishStep("still wrong"),
			finishStep("still wrong"),
			finishStep("still wrong"),
		}
	}
}

func (task scriptedTask) implPath() string {
	switch task.Lang {
	case "python":
		return "calc.py"
	case "go":
		return "answer.go"
	default:
		return "answer.js"
	}
}

func (task scriptedTask) testPath() string {
	switch task.Lang {
	case "python":
		return "tests/test_calc.py"
	case "go":
		return "answer_test.go"
	default:
		return "answer.test.js"
	}
}

func driveTask(t *testing.T, eng *testutil.Engine, st *store.Store, runID string, task scriptedTask) store.Run {
	t.Helper()
	limit := 90 * time.Second
	if task.Lang == "go" {
		limit = 3 * time.Minute
	}
	current := runUntilStop(t, eng, st, runID, limit)
	if task.Kind != "false_success" {
		return current
	}
	if current.State != run.AwaitingApproval {
		testutil.Dump(t, st, runID)
		t.Fatalf("%s state %s, want AWAITING_APPROVAL", task.Name, current.State)
	}
	approvePending(t, st, runID)
	return runUntilStop(t, eng, st, runID, limit)
}

func runUntilStop(t *testing.T, eng *testutil.Engine, st *store.Store, runID string, limit time.Duration) store.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if err := eng.Orch.Run(ctx, runID); err != nil {
		testutil.Dump(t, st, runID)
		t.Fatalf("run: %v", err)
	}
	return mustRun(t, st, runID)
}

func approvePending(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	cmds, err := st.ListCommands(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var pending store.Command
	found := false
	for _, cmd := range cmds {
		if cmd.State == "awaiting_approval" {
			pending = cmd
			found = true
		}
	}
	if !found {
		testutil.Dump(t, st, runID)
		t.Fatal("no command awaiting approval")
	}
	if _, err := st.Approve(context.Background(), store.Approval{
		ID:        id.New("apr_"),
		RunID:     runID,
		CommandID: pending.CommandID,
		ArgsHash:  pending.ArgsHash,
		Approver:  "eval",
		Decision:  "approved",
	}); err != nil {
		t.Fatal(err)
	}
}

func classifyTask(t *testing.T, st *store.Store, task scriptedTask, current store.Run, result repairResult, hiddenOK bool) string {
	t.Helper()
	if !result.Baseline.Recorded || result.Baseline.ExitCode == 0 {
		testutil.Dump(t, st, current.ID)
		t.Fatalf("%s baseline %+v", task.Name, result.Baseline)
	}
	switch task.Kind {
	case "repair", "recovery":
		if current.State != run.Succeeded || result.Outcome != "repaired" || !result.Tests.Passed || !hiddenOK || len(result.TestFiles) != 0 {
			testutil.Dump(t, st, current.ID)
			t.Fatalf("%s state %s outcome %s passed %v hidden %v test_files %v", task.Name, current.State, result.Outcome, result.Tests.Passed, hiddenOK, result.TestFiles)
		}
		if task.Kind == "recovery" && countEvents(t, st, current.ID, "verification.failed") < 1 {
			testutil.Dump(t, st, current.ID)
			t.Fatalf("%s did not return after a failed verification", task.Name)
		}
		return "solved"
	case "false_success":
		if current.State != run.Succeeded || result.Outcome != "passed_after_test_edit" || !result.Tests.Passed || hiddenOK || len(result.TestFiles) == 0 {
			testutil.Dump(t, st, current.ID)
			t.Fatalf("%s state %s outcome %s passed %v hidden %v test_files %v", task.Name, current.State, result.Outcome, result.Tests.Passed, hiddenOK, result.TestFiles)
		}
		return "false_success"
	default:
		if current.State != run.FailedTerminal || result.Tests.Passed || hiddenOK {
			testutil.Dump(t, st, current.ID)
			t.Fatalf("%s state %s outcome %s passed %v hidden %v", task.Name, current.State, result.Outcome, result.Tests.Passed, hiddenOK)
		}
		if result.Outcome != "repeated_verification" && result.Outcome != "verification_failed" {
			testutil.Dump(t, st, current.ID)
			t.Fatalf("%s outcome %s", task.Name, result.Outcome)
		}
		return "honest_failure"
	}
}

func writeTaskRepo(task scriptedTask, repo string) error {
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	files := map[string]string{}
	switch task.Lang {
	case "python":
		files["calc.py"] = "def answer(n):\n" + buggyLine(task.Lang, task.K)
		files["tests/__init__.py"] = ""
		files["tests/test_calc.py"] = fmt.Sprintf(`import unittest

from calc import answer


class CalcTest(unittest.TestCase):
    def test_visible(self):
%s

if __name__ == "__main__":
    unittest.main()
`, visibleAssert(task, task.Visible+task.K))
	case "go":
		files["go.mod"] = "module example.com/calceval\n\ngo 1.23.0\n"
		files["answer.go"] = "package calc\n\nfunc Answer(n int) int {\n" + buggyLine(task.Lang, task.K) + "}\n"
		files["answer_test.go"] = fmt.Sprintf(`package calc

import "testing"

func TestVisible(t *testing.T) {
	const n = %d
	const want = %d
	if got := Answer(n); got != want {
		t.Fatalf("got %%d want %%d", got, want)
	}
}
`, task.Visible, task.Visible+task.K)
	default:
		files["answer.js"] = "function answer(n) {\n" + buggyLine(task.Lang, task.K) + "}\n\nmodule.exports = { answer };\n"
		files["answer.test.js"] = fmt.Sprintf(`const test = require("node:test");
const assert = require("node:assert/strict");
const { answer } = require("./answer.js");

test("visible", () => {
%s});
`, visibleAssert(task, task.Visible+task.K))
	}
	for rel, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func visibleAssert(task scriptedTask, want int) string {
	switch task.Lang {
	case "python":
		return fmt.Sprintf("        self.assertEqual(answer(%d), %d)\n", task.Visible, want)
	case "go":
		return fmt.Sprintf("\tif got := Answer(%d); got != %d {\n", task.Visible, want)
	default:
		return fmt.Sprintf("  assert.equal(answer(%d), %d);\n", task.Visible, want)
	}
}

func buggyLine(lang string, k int) string {
	return implLine(lang, fmt.Sprintf("n - %d", k))
}

func fixedLine(lang string, k int) string {
	return implLine(lang, fmt.Sprintf("n + %d", k))
}

func commentLine(lang string, k int) string {
	switch lang {
	case "python":
		return fmt.Sprintf("    return n - %d  # still wrong\n", k)
	case "go":
		return fmt.Sprintf("\treturn n - %d // still wrong\n", k)
	default:
		return fmt.Sprintf("  return n - %d; // still wrong\n", k)
	}
}

func implLine(lang, expr string) string {
	switch lang {
	case "python":
		return "    return " + expr + "\n"
	case "go":
		return "\treturn " + expr + "\n"
	default:
		return "  return " + expr + ";\n"
	}
}

func runTestStep() scriptStep {
	return scriptStep{Tool: "run_test", Args: json.RawMessage(`{}`)}
}

func finishStep(text string) scriptStep {
	return scriptStep{Finish: true, Text: text}
}

func patchStep(path, old, new string) scriptStep {
	raw, err := json.Marshal(map[string]string{"path": path, "old": old, "new": new})
	if err != nil {
		panic(err)
	}
	return scriptStep{Tool: "apply_patch", Args: raw}
}

func hiddenPasses(t *testing.T, task scriptedTask, worktree string) bool {
	t.Helper()
	if worktree == "" {
		t.Fatal("worktree is empty")
	}
	dir := t.TempDir()
	switch task.Lang {
	case "python":
		copyFile(t, filepath.Join(worktree, "calc.py"), filepath.Join(dir, "calc.py"))
		body := fmt.Sprintf("from calc import answer\nimport sys\ngot = answer(%d)\nwant = %d\nif got != want:\n    sys.stderr.write('hidden %%s != %%s\\n' %% (got, want))\n    sys.exit(1)\n", task.Visible+100, task.Visible+100+task.K)
		writeFile(t, filepath.Join(dir, "hidden_check.py"), body)
		return commandOK(t, dir, "python3", "hidden_check.py")
	case "go":
		copyFile(t, filepath.Join(worktree, "go.mod"), filepath.Join(dir, "go.mod"))
		copyFile(t, filepath.Join(worktree, "answer.go"), filepath.Join(dir, "answer.go"))
		body := fmt.Sprintf("package calc\n\nimport \"testing\"\n\nfunc TestHidden(t *testing.T) {\n\tconst n = %d\n\tconst want = %d\n\tif got := Answer(n); got != want {\n\t\tt.Fatalf(\"hidden got %%d want %%d\", got, want)\n\t}\n}\n", task.Visible+100, task.Visible+100+task.K)
		writeFile(t, filepath.Join(dir, "hidden_test.go"), body)
		return commandOK(t, dir, "go", "test", ".")
	default:
		copyFile(t, filepath.Join(worktree, "answer.js"), filepath.Join(dir, "answer.js"))
		body := fmt.Sprintf("const { answer } = require(\"./answer.js\");\nconst got = answer(%d);\nconst want = %d;\nif (got !== want) {\n  console.error(`hidden ${got} != ${want}`);\n  process.exit(1);\n}\n", task.Visible+100, task.Visible+100+task.K)
		writeFile(t, filepath.Join(dir, "hidden.js"), body)
		return commandOK(t, dir, "node", "hidden.js")
	}
}

func commandOK(t *testing.T, dir, name string, args ...string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PYTHONDONTWRITEBYTECODE=1",
		"GO111MODULE=on",
		"CGO_ENABLED=0",
		"GOTOOLCHAIN=local",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	t.Logf("hidden %s failed: %s", name, out)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	t.Fatalf("hidden check %s: %v\n%s", name, err, out)
	return false
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, string(body))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(before, after map[string]string) bool {
	if len(before) != len(after) {
		return false
	}
	for rel, body := range before {
		if after[rel] != body {
			return false
		}
	}
	return true
}

func latencyStats(samples []time.Duration) (min, max, total time.Duration) {
	min = samples[0]
	for _, sample := range samples {
		total += sample
		if sample < min {
			min = sample
		}
		if sample > max {
			max = sample
		}
	}
	return min, max, total
}
