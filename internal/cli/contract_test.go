package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/verify"
)

func TestLoadContractStaysOnTheClient(t *testing.T) {
	if _, err := loadContract(verify.TaskRepair, `{"version":"verify.v1","checks":[]}`, ""); err == nil || !strings.Contains(err.Error(), "repair") {
		t.Fatalf("repair contract: %v", err)
	}
	if _, err := loadContract(verify.TaskFeature, "", ""); err == nil || !strings.Contains(err.Error(), "verification contract") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := loadContract(verify.TaskFeature, `{"version":"verify.v1"}`, "contract.json"); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("both: %v", err)
	}
	if _, err := loadContract(verify.TaskFeature, "eval/fixtures/featuregate/contract.json", ""); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("path text: %v", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "contract.json")
	body := []byte(`{"version":"verify.v1","checks":[]}`)
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadContract(verify.TaskRefactor, "", link); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink: %v", err)
	}
	got, err := loadContract(verify.TaskFeature, "", file)
	if err != nil || !strings.Contains(string(got), "verify.v1") {
		t.Fatalf("file: %s %v", got, err)
	}

	repo := t.TempDir()
	server := "http://127.0.0.1:1"
	errText := capture(t, &os.Stderr, func() {
		code := featureCmd([]string{"--server", server, "--repo", repo, "--profile", "python", "--test-arg", "python3", "--test-arg", "-m", "--test-arg", "unittest", "--test-arg", "tests.test_regress", "make greet return hello"})
		if code != 2 {
			t.Fatalf("feature exit %d", code)
		}
	})
	if !strings.Contains(errText, "verification contract") || strings.Contains(errText, "started agtd") {
		t.Fatalf("feature stderr %s", errText)
	}
	errText = capture(t, &os.Stderr, func() {
		code := fix([]string{"--server", server, "--repo", repo, "--profile", "python", "--test-arg", "python3", "--test-arg", "-m", "--test-arg", "unittest", "--test-arg", "tests.test_regress", "--contract-json", string(body), "repair it"})
		if code != 2 {
			t.Fatalf("fix exit %d", code)
		}
	})
	if !strings.Contains(errText, "repair") || strings.Contains(errText, "started agtd") {
		t.Fatalf("fix stderr %s", errText)
	}
	errText = capture(t, &os.Stderr, func() {
		code := sessionSubmit([]string{
			"ses_local", "--server", server, "--repo", repo, "--goal", "what does greet do",
			"--profile", "python", "--test-arg", "python3", "--test-arg", "-m", "--test-arg", "unittest", "--test-arg", "tests.test_regress",
			"--interaction", "ask", "--task", "feature", "--message-id", "msg_ask",
		})
		if code != 2 {
			t.Fatalf("ask feature exit %d", code)
		}
	})
	if !strings.Contains(errText, "read-only") || strings.Contains(errText, "started agtd") {
		t.Fatalf("ask stderr %s", errText)
	}
	errText = capture(t, &os.Stderr, func() {
		code := sessionSubmit([]string{
			"ses_local", "--server", server, "--repo", repo, "--goal", "plan the change",
			"--profile", "python", "--test-arg", "python3", "--test-arg", "-m", "--test-arg", "unittest", "--test-arg", "tests.test_regress",
			"--interaction", "plan", "--contract-file", file, "--message-id", "msg_plan",
		})
		if code != 2 {
			t.Fatalf("plan contract exit %d", code)
		}
	})
	if !strings.Contains(errText, "read-only") || strings.Contains(errText, "started agtd") {
		t.Fatalf("plan stderr %s", errText)
	}
}

func TestVerificationLinesShowTaskChecksAndFailures(t *testing.T) {
	lines := verificationLines(runView{
		TaskType:            "feature",
		VerificationOutcome: "acceptance_failed",
		ContractDigest:      "abc",
		Contract:            []byte(`{"version":"verify.v1","checks":[{"name":"accept","role":"acceptance","argv":["python3","-m","unittest","tests.test_accept"],"paths":["tests/test_accept.py"]}]}`),
		Final:               []byte(`{"phase":"final","checks":[{"name":"accept","role":"acceptance","exit_code":1,"kind":"fail"}]}`),
	})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"task feature", "verification acceptance_failed", "contract abc", "check acceptance accept", "failure acceptance accept fail exit 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}
