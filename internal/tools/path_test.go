package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeRelRejectsEscapes(t *testing.T) {
	bad := []string{
		"",
		".",
		"..",
		"../outside",
		"foo/../../etc/passwd",
		"/etc/passwd",
		`C:\Users\x\.ssh\id_rsa`,
		"C:/Users/x/.ssh/id_rsa",
		`\\server\share\secret`,
		"//server/share/secret",
		"dir/file.txt:stream",
		"notes/a\x00b",
	}
	for _, path := range bad {
		if _, err := SafeRel(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}

func TestSafeRelAllowsSpacesAndUnicode(t *testing.T) {
	for _, path := range []string{"notes/my file.txt", "笔记/文件.txt", "payments/processor.py"} {
		got, err := SafeRel(path)
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		if filepath.ToSlash(got) != path {
			t.Fatalf("clean %q want %q", got, path)
		}
	}
}

func TestReadAllowsContainedNamesAndRefusesSecrets(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"notes/my file.txt", "笔记/文件.txt"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadRange(root, rel, 1, 1)
		if err != nil || !strings.Contains(got, "hello") {
			t.Fatalf("read %s: %q %v", rel, got, err)
		}
	}
	secret := filepath.Join(root, ".env")
	if err := os.WriteFile(secret, []byte("TOKEN=hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRange(root, ".env", 1, 5); err == nil {
		t.Fatal("read .env")
	}
	if _, err := Patch(root, ".env", "TOKEN=hidden\n", "TOKEN=pwned\n"); err == nil {
		t.Fatal("patched .env")
	}
	body, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "TOKEN=hidden\n" {
		t.Fatalf("secret changed to %q", body)
	}
	abs := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(abs, []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(abs) })
	if _, err := Patch(root, abs, "nope\n", "pwned\n"); err == nil {
		t.Fatal("absolute path was written")
	}
}

func TestSearchSkipsSecretsAndBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "id_rsa"), []byte("UNIQUE_SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("UNIQUE_SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("TOKEN=example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte("UNIQUE_SECRET\x00more"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("UNIQUE_SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Search(root, "UNIQUE_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "app.py:") {
		t.Fatalf("search %q", out)
	}
	if strings.Contains(out, ".env") || strings.Contains(out, "id_rsa") || strings.Contains(out, "binary.dat") {
		t.Fatalf("search leaked %q", out)
	}
	example, err := Search(root, "TOKEN=example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(example, ".env.example:") {
		t.Fatalf("example search %q", example)
	}
}
