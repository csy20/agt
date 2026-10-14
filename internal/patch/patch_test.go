package patch

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildKeepsExactBytesModesAndDeletes(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	mustWrite(t, filepath.Join(base, "keep.txt"), []byte("same\n"), 0o644)
	mustWrite(t, filepath.Join(work, "keep.txt"), []byte("same\n"), 0o644)
	mustWrite(t, filepath.Join(base, "edit.txt"), []byte("old\r\n"), 0o644)
	mustWrite(t, filepath.Join(work, "edit.txt"), []byte("new"), 0o644)
	mustWrite(t, filepath.Join(base, "gone.txt"), []byte("bye\n"), 0o644)
	mustWrite(t, filepath.Join(work, "script.sh"), []byte("#!/bin/sh\n"), 0o755)
	mustWrite(t, filepath.Join(work, "bin.dat"), []byte{'a', 0, 'b'}, 0o644)

	art, err := Build(base, work, Meta{RunID: "run_1", RepoPath: "/repo", Revision: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if art.Contract != Contract || art.Strategy != Strategy || art.Revision != "abc" {
		t.Fatalf("artifact %#v", art)
	}
	if art.Digest != Digest(art) || art.BaseDigest == "" {
		t.Fatal("digest was not bound")
	}
	ops := map[string]File{}
	for _, f := range art.Files {
		ops[f.Path] = f
	}
	if _, ok := ops["keep.txt"]; ok {
		t.Fatal("unchanged file was included")
	}
	edit := ops["edit.txt"]
	if edit.Op != OpModify || edit.BeforeSHA256 == edit.AfterSHA256 {
		t.Fatalf("edit %#v", edit)
	}
	if string(mustDecode(t, edit.ContentBase64)) != "new" {
		t.Fatalf("edit bytes %q", mustDecode(t, edit.ContentBase64))
	}
	if ops["gone.txt"].Op != OpDelete || ops["gone.txt"].ContentBase64 != "" || ops["gone.txt"].AfterSHA256 != "" {
		t.Fatalf("delete %#v", ops["gone.txt"])
	}
	if ops["script.sh"].Op != OpAdd || ops["script.sh"].AfterMode != "0755" {
		t.Fatalf("script %#v", ops["script.sh"])
	}
	if !bytes.Equal(mustDecode(t, ops["bin.dat"].ContentBase64), []byte{'a', 0, 'b'}) {
		t.Fatal("NUL byte was not preserved")
	}
	again, err := Build(base, work, Meta{RunID: "run_1", RepoPath: "/repo", Revision: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != art.Digest {
		t.Fatal("digest was not stable")
	}
}

func TestApplyPreservesDirtyFilesAndRejectsStaleOrEscape(t *testing.T) {
	base, work, repo := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(base, "a.txt"), []byte("old\n"), 0o644)
	mustWrite(t, filepath.Join(work, "a.txt"), []byte("new\n"), 0o644)
	mustWrite(t, filepath.Join(repo, "a.txt"), []byte("old\n"), 0o644)
	mustWrite(t, filepath.Join(base, "b.txt"), []byte("base\n"), 0o644)
	mustWrite(t, filepath.Join(work, "b.txt"), []byte("work\n"), 0o644)
	mustWrite(t, filepath.Join(repo, "b.txt"), []byte("base\n"), 0o644)
	mustWrite(t, filepath.Join(repo, "notes.txt"), []byte("dirty\n"), 0o644)
	mustWrite(t, filepath.Join(work, "added.txt"), []byte("hello"), 0o644)

	art, err := Build(base, work, Meta{RunID: "run_1", RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	rollback := filepath.Join(t.TempDir(), "rollback")
	bad := art
	bad.Digest = strings.Repeat("ab", 32)
	if err := Apply(repo, bad, rollback); !errors.Is(err, ErrDigest) {
		t.Fatalf("digest err %v", err)
	}
	if _, err := os.Stat(rollback); !os.IsNotExist(err) {
		t.Fatal("rejected digest created a rollback directory")
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "old\n" {
		t.Fatalf("digest mismatch wrote %q", got)
	}

	escaped := art
	escaped.Files = append([]File(nil), art.Files...)
	body := []byte("pwn")
	escaped.Files = append(escaped.Files, File{
		Op: OpAdd, Path: "../outside.txt", AfterMode: "0644",
		AfterSHA256: hashBytes(body), ContentBase64: base64.StdEncoding.EncodeToString(body),
	})
	escaped.Digest = Digest(escaped)
	if err := Apply(repo, escaped, filepath.Join(t.TempDir(), "escape")); !errors.Is(err, ErrEscape) {
		t.Fatalf("escape err %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("escape wrote outside the repository")
	}
	linkRepo := t.TempDir()
	linkOutside := t.TempDir()
	if err := os.Symlink(linkOutside, filepath.Join(linkRepo, "link")); err != nil {
		t.Fatal(err)
	}
	linked := Artifact{
		Contract: Contract, Strategy: Strategy, RunID: "run_1", RepoPath: linkRepo,
		Files: []File{{
			Op: OpAdd, Path: "link/pwned.txt", AfterMode: "0644",
			AfterSHA256: hashBytes(body), ContentBase64: base64.StdEncoding.EncodeToString(body),
		}},
	}
	linked.Digest = Digest(linked)
	if err := Apply(linkRepo, linked, filepath.Join(t.TempDir(), "link-rollback")); !errors.Is(err, ErrEscape) {
		t.Fatalf("symlink err %v", err)
	}
	if _, err := os.Stat(filepath.Join(linkOutside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatal("symlink escape wrote outside the repository")
	}

	staleRepo := t.TempDir()
	mustWrite(t, filepath.Join(staleRepo, "a.txt"), []byte("user\n"), 0o644)
	mustWrite(t, filepath.Join(staleRepo, "b.txt"), []byte("base\n"), 0o644)
	if err := Apply(staleRepo, art, filepath.Join(t.TempDir(), "stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale err %v", err)
	}
	if got := readFile(t, filepath.Join(staleRepo, "a.txt")); got != "user\n" {
		t.Fatalf("stale apply wrote %q", got)
	}
	if _, err := os.Stat(filepath.Join(staleRepo, "added.txt")); !os.IsNotExist(err) {
		t.Fatal("stale apply created a later file")
	}
	if got := readFile(t, filepath.Join(staleRepo, "b.txt")); got != "base\n" {
		t.Fatalf("stale apply wrote an earlier file %q", got)
	}

	if err := Apply(repo, art, rollback); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "new\n" {
		t.Fatalf("a.txt %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "b.txt")); got != "work\n" {
		t.Fatalf("b.txt %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "added.txt")); got != "hello" {
		t.Fatalf("added %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "notes.txt")); got != "dirty\n" {
		t.Fatalf("unrelated file %q", got)
	}
	if _, err := os.Stat(filepath.Join(rollback, "manifest.json")); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(repo, "a.txt"), []byte("newer\n"), 0o644)
	if err := Apply(repo, art, filepath.Join(t.TempDir(), "again")); !errors.Is(err, ErrConflict) {
		t.Fatalf("second apply err %v", err)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "newer\n" {
		t.Fatalf("second apply overwrote %q", got)
	}
}

func TestApplyRollsBackAPartialWrite(t *testing.T) {
	base, work, repo := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(base, "a.txt"), []byte("old-a\n"), 0o644)
	mustWrite(t, filepath.Join(work, "a.txt"), []byte("new-a\n"), 0o644)
	mustWrite(t, filepath.Join(repo, "a.txt"), []byte("old-a\n"), 0o644)
	mustWrite(t, filepath.Join(base, "b.txt"), []byte("old-b\n"), 0o644)
	mustWrite(t, filepath.Join(work, "b.txt"), []byte("new-b\n"), 0o644)
	mustWrite(t, filepath.Join(repo, "b.txt"), []byte("old-b\n"), 0o644)
	art, err := Build(base, work, Meta{RunID: "run_1", RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	orig := beforeMutate
	t.Cleanup(func() { beforeMutate = orig })
	n := 0
	beforeMutate = func(string) error {
		n++
		if n == 2 {
			return errors.New("stopped")
		}
		return nil
	}
	err = Apply(repo, art, filepath.Join(t.TempDir(), "rollback"))
	if err == nil || !strings.Contains(err.Error(), "restored 1") {
		t.Fatalf("err %v", err)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "old-a\n" {
		t.Fatalf("a.txt after rollback %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "b.txt")); got != "old-b\n" {
		t.Fatalf("b.txt after rollback %q", got)
	}
}

func TestApplyKeepsModeOnlyChanges(t *testing.T) {
	base, work, repo := t.TempDir(), t.TempDir(), t.TempDir()
	body := []byte("#!/bin/sh\n")
	mustWrite(t, filepath.Join(base, "script.sh"), body, 0o644)
	mustWrite(t, filepath.Join(work, "script.sh"), body, 0o755)
	mustWrite(t, filepath.Join(repo, "script.sh"), body, 0o644)
	art, err := Build(base, work, Meta{RunID: "run_1", RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(art.Files) != 1 || art.Files[0].Op != OpModify || art.Files[0].AfterMode != "0755" {
		t.Fatalf("files %#v", art.Files)
	}
	if err := Apply(repo, art, filepath.Join(t.TempDir(), "rollback")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(repo, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func mustWrite(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
