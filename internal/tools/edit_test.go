package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileIsHashedAtomicAndIdempotent(t *testing.T) {
	root := t.TempDir()
	created, err := WriteFile(root, WriteArgs{Path: "notes/a.txt", Content: "one\n", BeforeSHA256: ""})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, created, "created")
	body, err := os.ReadFile(filepath.Join(root, "notes", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "one\n" {
		t.Fatalf("body %q", body)
	}
	before := SHA256(body)
	again, err := WriteFile(root, WriteArgs{Path: "notes/a.txt", Content: "one\n", BeforeSHA256: ""})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, again, "already_applied")

	stale, err := WriteFile(root, WriteArgs{Path: "notes/a.txt", Content: "two\n", BeforeSHA256: strings.Repeat("ab", 32)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale err %v", err)
	}
	assertAction(t, stale, "conflict")
	if got, _ := os.ReadFile(filepath.Join(root, "notes", "a.txt")); string(got) != "one\n" {
		t.Fatalf("stale write changed the file to %q", got)
	}

	if err := os.Chmod(filepath.Join(root, "notes", "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	written, err := WriteFile(root, WriteArgs{Path: "notes/a.txt", Content: "two\n", BeforeSHA256: before})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, written, "written")
	info, err := os.Stat(filepath.Join(root, "notes", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	retry, err := WriteFile(root, WriteArgs{Path: "notes/a.txt", Content: "two\n", BeforeSHA256: before})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, retry, "already_applied")
	matches, err := filepath.Glob(filepath.Join(root, "notes", ".agt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}

	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(root, WriteArgs{Path: "leak", Content: "pwned", BeforeSHA256: SHA256([]byte("secret"))}); err == nil {
		t.Fatal("symlink write should fail")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "secret" {
		t.Fatalf("symlink target %q %v", got, err)
	}
}

func TestDeleteFileIsHashedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(path, []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := SHA256([]byte("bye\n"))
	if _, err := DeleteFile(root, DeleteArgs{Path: "gone.txt", BeforeSHA256: strings.Repeat("cd", 32)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteFile(root, DeleteArgs{Path: "gone.txt", BeforeSHA256: sum})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, deleted, "deleted")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
	again, err := DeleteFile(root, DeleteArgs{Path: "gone.txt", BeforeSHA256: sum})
	if err != nil {
		t.Fatal(err)
	}
	assertAction(t, again, "already_applied")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteFile(root, DeleteArgs{Path: "dir", BeforeSHA256: sum}); err == nil {
		t.Fatal("deleted a directory")
	}
}

func TestWriteAndDeleteShowInTheDiff(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "keep.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(work, WriteArgs{Path: "new.txt", Content: "fresh\n"}); err != nil {
		t.Fatal(err)
	}
	sum := SHA256([]byte("old\n"))
	if _, err := DeleteFile(work, DeleteArgs{Path: "keep.txt", BeforeSHA256: sum}); err != nil {
		t.Fatal(err)
	}
	diff, err := Diff(base, work)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "new.txt") || !strings.Contains(diff, "keep.txt") {
		t.Fatalf("diff %s", diff)
	}
}

func assertAction(t *testing.T, body, want string) {
	t.Helper()
	var got FileResult
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != want || got.Path == "" {
		t.Fatalf("result %+v, want action %s", got, want)
	}
}
