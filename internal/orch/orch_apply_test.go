package orch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/testutil"
)

func TestReviewedApplyUpdatesACopyAndRejectsAStalePatch(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	repo := t.TempDir()
	copyRepo(t, fixture, repo)
	sha := strings.Repeat("cd", 20)
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
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	created := eng.Create(t, "make the retry test pass", repo, 100, 16, testutil.FixScript(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current, err := st.GetRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.Succeeded {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	if string(readProcessor(t, repo)) != string(original) {
		t.Fatal("source copy changed before apply")
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("fixture changed")
	}
	if _, err := os.Stat(filepath.Join(current.Worktree, ".git")); !os.IsNotExist(err) {
		t.Fatal("worktree contains .git")
	}
	notes, err := os.ReadFile(filepath.Join(current.Worktree, "notes.txt"))
	if err != nil || string(notes) != "uncommitted\n" {
		t.Fatalf("uncommitted file %q %v", notes, err)
	}
	found := false
	events, err := st.EventsAfter(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != "workspace.captured" {
			continue
		}
		found = true
		var payload struct {
			Strategy string `json:"strategy"`
			Revision string `json:"revision"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Strategy != patch.Strategy || payload.Revision != sha {
			t.Fatalf("capture %#v", payload)
		}
	}
	if !found {
		t.Fatal("workspace was not captured")
	}

	art, err := patch.Build(orch.BasePath(current.Worktree), current.Worktree, patch.Meta{
		RunID: current.ID, RepoPath: current.RepoPath, Revision: sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := art
	bad.Digest = strings.Repeat("ab", 32)
	if err := patch.Apply(repo, bad, filepath.Join(t.TempDir(), "bad")); !errors.Is(err, patch.ErrDigest) {
		t.Fatalf("digest err %v", err)
	}
	if string(readProcessor(t, repo)) != string(original) {
		t.Fatal("rejected digest changed the source")
	}
	if err := patch.Apply(repo, art, filepath.Join(t.TempDir(), "rollback")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readProcessor(t, repo)), "self.charged") {
		t.Fatal("apply did not write the repair")
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("fixture changed during apply")
	}
	if notes, err = os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || string(notes) != "uncommitted\n" {
		t.Fatalf("notes %q %v", notes, err)
	}

	patched := readProcessor(t, repo)
	patched = append([]byte(nil), patched...)
	patched = append(patched, []byte("\n# local edit\n")...)
	if err := os.WriteFile(filepath.Join(repo, "payments", "processor.py"), patched, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := patch.Apply(repo, art, filepath.Join(t.TempDir(), "stale")); !errors.Is(err, patch.ErrConflict) {
		t.Fatalf("stale err %v", err)
	}
	if got := readProcessor(t, repo); string(got) != string(patched) {
		t.Fatal("stale apply overwrote newer user work")
	}
}

func copyRepo(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, body, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
}
