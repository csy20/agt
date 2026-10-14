package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeRejectsNestedDataAndRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("stay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(repo, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Materialize(repo, data, "run_nested"); err == nil || !strings.Contains(err.Error(), "data directory is inside the repository") {
		t.Fatalf("data inside repo: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(repo, "keep.txt")); err != nil || string(body) != "stay\n" {
		t.Fatalf("source changed: %q %v", body, err)
	}

	outer := t.TempDir()
	inner := filepath.Join(outer, "repo")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Materialize(inner, outer, "run_inner"); err == nil || !strings.Contains(err.Error(), "repository is inside the data directory") {
		t.Fatalf("repo inside data: %v", err)
	}
}

func TestMaterializeCopiesUncommittedFilesAndReadsHEAD(t *testing.T) {
	repo := t.TempDir()
	sha := strings.Repeat("ab", 20)
	refs := filepath.Join(repo, ".git", "refs", "heads")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refs, "main"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := ReadCapture(repo)
	if cap.Revision != sha || cap.Ref != "refs/heads/main" {
		t.Fatalf("capture %#v", cap)
	}
	work, base, err := Materialize(repo, t.TempDir(), "run_git")
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(work, "notes.txt")); err != nil || string(body) != "local\n" {
		t.Fatalf("work notes %q %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(base, "notes.txt")); err != nil || string(body) != "local\n" {
		t.Fatalf("base notes %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(work, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git was copied")
	}
	if body, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || string(body) != "local\n" {
		t.Fatal("source was modified")
	}

	packed := t.TempDir()
	git := filepath.Join(packed, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("cd", 20)
	if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/topic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(git, "packed-refs"), []byte("# pack-refs with: peeled\n"+other+" refs/heads/topic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadCapture(packed)
	if got.Revision != other || got.Ref != "refs/heads/topic" {
		t.Fatalf("packed %#v", got)
	}
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cap := ReadCapture(plain); cap.Revision != "" || cap.Ref != "" {
		t.Fatalf("non-git %#v", cap)
	}
	if _, _, err := Materialize(plain, t.TempDir(), "run_plain"); err != nil {
		t.Fatal(err)
	}
}
