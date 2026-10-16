package verify

import (
	"strings"
	"testing"
)

func featureRaw() string {
	return `{
		"version": "verify.v1",
		"checks": [
			{"name": "regress", "role": "regression", "argv": ["python", "-m", "unittest", "tests.test_regress"], "paths": ["tests/test_regress.py"]},
			{"name": "accept", "role": "acceptance", "argv": ["python", "-m", "unittest", "tests.test_accept"], "paths": ["tests/test_accept.py"]}
		]
	}`
}

func TestParseRejectsEmptyEscapeAndProfileMismatch(t *testing.T) {
	if _, _, _, err := Parse(nil, TaskFeature, "python"); err == nil {
		t.Fatal("empty feature contract")
	}
	if _, _, _, err := Parse([]byte(`{"version":"verify.v1","checks":[]}`), TaskFeature, "python"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatal(err)
	}
	if _, _, _, err := Parse([]byte(featureRaw()), TaskRepair, "python"); err == nil || !strings.Contains(err.Error(), "repair") {
		t.Fatal(err)
	}
	if _, _, digest, err := Parse(nil, TaskRepair, "python"); err != nil || digest != "" {
		t.Fatalf("repair digest %q err %v", digest, err)
	}
	path := `{"version":"verify.v1","checks":[{"name":"accept","role":"acceptance","argv":["python","-m","unittest","tests.test_accept"],"paths":["../secret.py"]},{"name":"regress","role":"regression","argv":["python","-m","unittest","tests.test_regress"],"paths":["tests/test_regress.py"]}]}`
	if _, _, _, err := Parse([]byte(path), TaskFeature, "python"); err == nil {
		t.Fatal("accepted an escaping path")
	}
	abs := strings.Replace(path, "../secret.py", "/etc/passwd", 1)
	if _, _, _, err := Parse([]byte(abs), TaskFeature, "python"); err == nil {
		t.Fatal("accepted an absolute path")
	}
	slash := strings.Replace(path, "../secret.py", `tests\\test_accept.py`, 1)
	if _, _, _, err := Parse([]byte(slash), TaskFeature, "python"); err == nil {
		t.Fatal("accepted a backslash path")
	}
	if _, _, _, err := Parse([]byte(`"/tmp/contract.json"`), TaskFeature, "python"); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatal(err)
	}
	mismatch := strings.Replace(featureRaw(), `"python"`, `"go"`, 1)
	if _, _, _, err := Parse([]byte(mismatch), TaskFeature, "python"); err == nil {
		t.Fatal("accepted a go argv on a python profile")
	}
	if _, _, _, err := Parse([]byte(featureRaw()), TaskRefactor, "python"); err == nil {
		t.Fatal("accepted feature roles for a refactor")
	}
	contract, canonical, digest, err := Parse([]byte(featureRaw()), TaskFeature, "python")
	if err != nil || digest == "" || len(canonical) == 0 || len(contract.Checks) != 2 {
		t.Fatal(err)
	}
	if _, _, again, err := Parse(canonical, TaskFeature, "python"); err != nil || again != digest {
		t.Fatalf("digest changed %s %s %v", digest, again, err)
	}
}

func TestClassifyKeepsInfrastructureOutOfTheRedBaseline(t *testing.T) {
	passOut := ".\n----------------------------------------------------------------------\nRan 1 test in 0.001s\n\nOK\n"
	failOut := "FAIL: test_hello\nAssertionError: 'hi' != 'hello'\n\nRan 1 test in 0.001s\n\nFAILED (failures=1)\n"
	if kind, _ := Classify(0, passOut, nil); kind != KindPass {
		t.Fatalf("pass %s", kind)
	}
	if kind, _ := Classify(1, failOut, nil); kind != KindFail {
		t.Fatalf("fail %s", kind)
	}
	empty := "----------------------------------------------------------------------\nRan 0 tests in 0.000s\n\nOK\n"
	if kind, reason := Classify(0, empty, nil); kind != KindInfrastructure || reason != "empty test run" {
		t.Fatalf("%s %s", kind, reason)
	}
	imported := "ImportError: Failed to import test module: tests.test_absent\nModuleNotFoundError: No module named 'tests.test_absent'\nFAILED (errors=1)\n"
	if kind, reason := Classify(1, imported, nil); kind != KindInfrastructure || !strings.Contains(reason, "not executed") {
		t.Fatalf("%s %s", kind, reason)
	}
	if kind, reason := Classify(124, failOut, nil); kind != KindInfrastructure || reason != "timeout" {
		t.Fatalf("timeout %s %s", kind, reason)
	}
	if kind, reason := Classify(127, "python: executable file not found", nil); kind != KindInfrastructure || reason != "missing executable" {
		t.Fatalf("missing %s %s", kind, reason)
	}
	if kind, _ := Classify(0, "[no test files]", nil); kind != KindInfrastructure {
		t.Fatalf("go empty %s", kind)
	}
	if kind, _ := Classify(1, "the model said tests passed", nil); kind != KindInfrastructure {
		t.Fatalf("unrecognized %s", kind)
	}
}

func TestDecisionsStayDistinct(t *testing.T) {
	pass := CheckResult{Name: "regress", Role: RoleRegression, Kind: KindPass}
	fail := CheckResult{Name: "accept", Role: RoleAcceptance, Kind: KindFail}
	acceptPass := CheckResult{Name: "accept", Role: RoleAcceptance, Kind: KindPass}
	infra := CheckResult{Name: "accept", Role: RoleAcceptance, Kind: KindInfrastructure, Reason: "empty test run"}
	broken := CheckResult{Name: "regress", Role: RoleRegression, Kind: KindFail}
	if got := DecideBaseline(TaskFeature, []CheckResult{pass, fail}); !got.Proceed {
		t.Fatalf("baseline %+v", got)
	}
	got := DecideBaseline(TaskFeature, []CheckResult{pass, acceptPass})
	if got.Outcome != OutcomeAlreadySatisfied || got.Pass || strings.Contains(got.Summary, "newly implemented feature") == false {
		t.Fatalf("satisfied %+v", got)
	}
	got = DecideBaseline(TaskFeature, []CheckResult{broken, acceptPass})
	if got.Outcome != OutcomeRegressionBaselineFailed || got.Proceed {
		t.Fatalf("regress %+v", got)
	}
	got = DecideBaseline(TaskFeature, []CheckResult{pass, infra})
	if got.Outcome != OutcomeVerificationInfra || got.Proceed {
		t.Fatalf("infra %+v", got)
	}
	protectedPass := CheckResult{Name: "keep", Role: RoleProtected, Kind: KindPass}
	protectedFail := CheckResult{Name: "keep", Role: RoleProtected, Kind: KindFail}
	if got := DecideBaseline(TaskRefactor, []CheckResult{protectedFail}); got.Outcome != OutcomeRefactorBaselineFailed || got.Pass {
		t.Fatalf("refactor baseline %+v", got)
	}
	final := DecideFinal(TaskFeature, []CheckResult{pass, acceptPass}, false, true)
	if !final.Pass || final.Outcome != OutcomeFeatureImplemented {
		t.Fatalf("implemented %+v", final)
	}
	final = DecideFinal(TaskFeature, []CheckResult{pass, acceptPass}, true, true)
	if final.Pass || final.Outcome != OutcomeContractWeakened {
		t.Fatalf("weakened %+v", final)
	}
	final = DecideFinal(TaskRefactor, []CheckResult{protectedPass}, false, true)
	if !final.Pass || final.Outcome != OutcomeChecksPassed || !strings.Contains(final.Summary, "not a proof") {
		t.Fatalf("checks %+v", final)
	}
	final = DecideFinal(TaskRefactor, []CheckResult{protectedFail}, false, true)
	if final.Pass || final.Outcome != OutcomeChecksFailed || !final.Retry {
		t.Fatalf("checks failed %+v", final)
	}
}
