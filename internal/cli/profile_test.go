package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixWithoutProfileDoesNotStartARun(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/x\n\ngo 1.23.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	errText := capture(t, &os.Stderr, func() {
		code = fix([]string{"--repo", repo, "repair the test"})
	})
	if code != 2 {
		t.Fatalf("exit %d\n%s", code, errText)
	}
	if !strings.Contains(errText, "requires --profile") || !strings.Contains(errText, "suggested profile: go") {
		t.Fatalf("stderr %s", errText)
	}
	if strings.Contains(errText, "http") {
		t.Fatalf("profile detection contacted a server:\n%s", errText)
	}
}

func TestPrepareAndShellArgvAreRejectedLocally(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := fix([]string{"--repo", repo, "--profile", "python", "--test-arg", "python", "--prepare", "goal"})
	if code != 2 {
		t.Fatalf("prepare exit %d", code)
	}
	errText := capture(t, &os.Stderr, func() {
		code = fix([]string{"--repo", repo, "--profile", "python", "--test-arg", "bash", "--test-arg", "-c", "goal"})
	})
	if code != 2 || !strings.Contains(errText, "python") {
		t.Fatalf("shell exit %d\n%s", code, errText)
	}
}

func TestProfileDetectPrintsASuggestion(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := capture(t, &os.Stdout, func() {
		code = profileCommand([]string{"detect", "--repo", repo})
	})
	if code != 0 || !strings.Contains(out, "suggested profile: javascript") || !strings.Contains(out, "does not start a run") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func capture(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	old := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*target = w
	fn()
	w.Close()
	*target = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
