package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestTuiDumpPrintsExampleData(t *testing.T) {
	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "tui", "--dump"})
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"--- approval ---",
		"--- plan ---",
		"example approval",
		"example plan",
		"Example data. Repository files were not changed.",
		"9f2ac0c1e4b8…",
		"edits that change test files require approval",
		"c0ffee00a1b2…",
		"self.charged",
		"test_retry_does_not_double_charge",
		"plan read-only",
		"estimate $",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(out, "9f2ac0c1e4b87a6d5c0e1f2233445566778899aabbccddeeff00112233445566") {
		t.Fatal("dump showed the full approval hash")
	}
	if strings.Contains(out, "\x1b") {
		t.Fatal("dump used color")
	}
}

func TestDumpStaysPlainWhenNO_COLORIsSet(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"agt", "tui", "--dump", "--color"})
	})
	if code != 0 || strings.Contains(out, "\x1b") {
		t.Fatalf("exit %d ansi %v", code, strings.Contains(out, "\x1b"))
	}
}

func TestPipedTuiRefusesTheSession(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	code := Run([]string{"agt", "tui"})
	_ = w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	body, _ := io.ReadAll(r)
	_ = r.Close()
	if code != 2 || !strings.Contains(string(body), "needs a terminal") {
		t.Fatalf("exit %d\n%s", code, body)
	}
	if strings.Contains(string(body), "--- approval ---") {
		t.Fatal("piped tui printed a session")
	}
}
