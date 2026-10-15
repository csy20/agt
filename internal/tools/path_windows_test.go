//go:build windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestJunctionEscapeIsNotRead is the Windows reparse-point check.
// It is not executed by the Linux test run that produced the status notes.
func TestJunctionEscapeIsNotRead(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, outside)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if _, err := ReadRange(root, "linked/secret.txt", 1, 5); err == nil {
		t.Fatal("read followed a junction out of the worktree")
	}
	body, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "secret-value\n" {
		t.Fatalf("outside file changed to %q", body)
	}
}
