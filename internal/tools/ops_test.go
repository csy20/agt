package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchIsIdempotentAndRefusesEscape(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "payments")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "processor.py")
	if err := os.WriteFile(file, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, err := Patch(root, "payments/processor.py", "alpha\n", "beta\n")
	if err != nil || msg != "patched" {
		t.Fatalf("first patch: %s %v", msg, err)
	}
	msg, err = Patch(root, "payments/processor.py", "alpha\n", "beta\n")
	if err != nil || msg != "already applied" {
		t.Fatalf("second patch: %s %v", msg, err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "beta\n" {
		t.Fatalf("content = %q", body)
	}

	if _, err := Patch(root, "../outside.txt", "a", "b"); err == nil {
		t.Fatal("parent path was written")
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file exists: %v", err)
	}
}

func TestSymlinkEscapeIsNotWritten(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "secret")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	_, err := Patch(root, "leak", "secret", "pwned")
	if err == nil {
		t.Fatal("symlink patch should fail")
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "secret" {
		t.Fatalf("target changed to %q", body)
	}
}

func TestSearchFindsLiteral(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "payments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payments", "processor.py"), []byte("def charge():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Search(root, "def charge")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "payments/processor.py:1:") {
		t.Fatalf("search = %q", out)
	}
}
