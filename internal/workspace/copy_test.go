package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeKeepsConfigModesAndSkipsDependencies(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "config"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "node_modules", "leftpad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "node_modules", "leftpad", "index.js"), []byte("module.exports=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "build", "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "build", "out", "app"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(src, "script.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script.sh", filepath.Join(src, "link.sh")); err == nil {
		// Symlinks are refused. Put the link last so the assertion is the error.
	}

	work, _, err := Materialize(src, t.TempDir(), "run_copy")
	if err == nil {
		t.Fatal("symlink was copied")
	}
	_ = work
}

func TestMaterializeSkipsSecretFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".ssh", "id_rsa"), []byte("PRIVATE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("TOKEN=hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".env.example"), []byte("TOKEN=example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".github", "workflows.yml"), []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work, _, err := Materialize(src, t.TempDir(), "run_secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".ssh", "id_rsa")); !os.IsNotExist(err) {
		t.Fatalf("key copied: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(work, ".env.example"))
	if err != nil || string(body) != "TOKEN=example\n" {
		t.Fatalf("example %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(work, ".github", "workflows.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeRejectsOversizedFile(t *testing.T) {
	src := t.TempDir()
	f, err := os.Create(filepath.Join(src, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxCopyFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, _, err := Materialize(src, t.TempDir(), "run_big"); err == nil || !strings.Contains(err.Error(), "copy limit") {
		t.Fatalf("err %v", err)
	}
}

func TestMaterializeCopiesDotfilesAndExecutableBits(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "node_modules", "leftpad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "node_modules", "leftpad", "index.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "cmd", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(src, "cmd", "tool", "run.sh")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}

	work, base, err := Materialize(src, t.TempDir(), "run_copy")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{work, base} {
		if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "ci.yml")); err != nil {
			t.Fatalf("config missing in %s: %v", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
			t.Fatalf("dotfile missing in %s: %v", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".git", "config")); !os.IsNotExist(err) {
			t.Fatalf(".git copied into %s: %v", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules", "leftpad", "index.js")); !os.IsNotExist(err) {
			t.Fatalf("node_modules copied into %s: %v", root, err)
		}
		st, err := os.Stat(filepath.Join(root, "cmd", "tool", "run.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o111 == 0 {
			t.Fatalf("executable bit cleared in %s: %o", root, st.Mode().Perm())
		}
	}
}
