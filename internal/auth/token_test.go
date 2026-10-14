package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFileIsPrivateAndStable(t *testing.T) {
	t.Setenv("AGT_TOKEN", "")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateToken(first); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token mode %o", info.Mode().Perm())
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("data dir mode %o", dirInfo.Mode().Perm())
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("token changed across loads")
	}
	got, err := Read(dir)
	if err != nil || got != first {
		t.Fatalf("read %q err %v", got, err)
	}
}

func TestLooseTokenIsRotated(t *testing.T) {
	t.Setenv("AGT_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	const leaked = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(leaked+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == leaked {
		t.Fatal("kept a group-readable token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == leaked+"\n" {
		t.Fatal("leaked token still on disk")
	}
}

func TestSymlinkedTokenIsRefused(t *testing.T) {
	t.Setenv("AGT_TOKEN", "")
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), fileName)
	const leaked = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(outside, []byte(leaked+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, fileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("followed a symlinked auth.token")
	}
	body, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != leaked+"\n" {
		t.Fatalf("symlink target changed to %q", body)
	}
}

func TestEnvironmentTokenIsStoredForTheClient(t *testing.T) {
	const want = "override-token-value"
	t.Setenv("AGT_TOKEN", want)
	dir := t.TempDir()
	got, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("token %q", got)
	}
	t.Setenv("AGT_TOKEN", "")
	read, err := Read(dir)
	if err != nil || read != want {
		t.Fatalf("read %q err %v", read, err)
	}
}
