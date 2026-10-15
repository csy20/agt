package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
	"github.com/csy20/agt/internal/workspace"
)

func TestReviewedApplyIsDigestBound(t *testing.T) {
	st := testutil.Open(t)
	data := t.TempDir()
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, "a.txt"), "old\n", 0o644)
	writeRepoFile(t, filepath.Join(repo, "c.txt"), "gone\n", 0o644)
	writeRepoFile(t, filepath.Join(repo, "keep.txt"), "keep\n", 0o644)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: data}, testToken).Handler())
	t.Cleanup(ts.Close)

	created := postRun(t, ts.URL, map[string]any{
		"goal": "review", "repo_path": repo, "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"profile":     pythonProfile(),
	}, "idem-review")
	if created.status != http.StatusAccepted {
		t.Fatalf("create %d %s", created.status, created.body)
	}
	current, err := st.GetRun(context.Background(), created.runID)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := workspace.Materialize(current.RepoPath, data, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(work, "a.txt"), "new\n", 0o644)
	if err := os.Remove(filepath.Join(work, "c.txt")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(work, "added.txt"), "hello", 0o644)
	if err := st.SetWorktree(context.Background(), current.ID, work); err != nil {
		t.Fatal(err)
	}

	early := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": "abc"}, "")
	earlyBody := readBody(t, early)
	if early.status != http.StatusConflict || !strings.Contains(earlyBody, "SUCCEEDED") {
		t.Fatalf("early apply %d %s", early.status, earlyBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "old\n" {
		t.Fatalf("early apply wrote %q", got)
	}

	view := getJSON(t, ts.URL+"/v1/runs/"+current.ID+"/diff")
	if view["digest"] == "" || view["complete"] != true {
		t.Fatalf("diff %#v", view)
	}
	diff, _ := view["diff"].(string)
	if !strings.Contains(diff, "new") || !strings.Contains(diff, "added.txt") {
		t.Fatalf("diff %s", diff)
	}
	patchView := getJSON(t, ts.URL+"/v1/runs/"+current.ID+"/patch")
	digest, _ := patchView["digest"].(string)
	if digest == "" || patchView["strategy"] != "snapshot-plus-patch" || patchView["repo_path"] != current.RepoPath {
		t.Fatalf("patch %#v", patchView)
	}

	current = advance(t, st, current, run.Contextualising, run.Planning, run.Verifying, run.Succeeded)
	chosen := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{
		"digest": digest, "repo_path": t.TempDir(),
	}, "")
	chosenBody := readBody(t, chosen)
	if chosen.status != http.StatusBadRequest {
		t.Fatalf("client repo %d %s", chosen.status, chosenBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "old\n" {
		t.Fatalf("client repo wrote %q", got)
	}

	wrong := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": "deadbeef"}, "")
	wrongBody := readBody(t, wrong)
	if wrong.status != http.StatusConflict || !strings.Contains(wrongBody, "digest") {
		t.Fatalf("wrong digest %d %s", wrong.status, wrongBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "old\n" {
		t.Fatalf("wrong digest wrote %q", got)
	}

	writeRepoFile(t, filepath.Join(repo, "keep.txt"), "dirty\n", 0o644)
	ok := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": digest}, "")
	okBody := readBody(t, ok)
	if ok.status != http.StatusOK {
		t.Fatalf("apply %d %s", ok.status, okBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "new\n" {
		t.Fatalf("a.txt %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("deleted file remains")
	}
	if got := readRepoFile(t, filepath.Join(repo, "added.txt")); got != "hello" {
		t.Fatalf("added %q", got)
	}
	if got := readRepoFile(t, filepath.Join(repo, "keep.txt")); got != "dirty\n" {
		t.Fatalf("dirty file %q", got)
	}

	writeRepoFile(t, filepath.Join(repo, "a.txt"), "newer\n", 0o644)
	stale := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": digest}, "")
	staleBody := readBody(t, stale)
	if stale.status != http.StatusConflict {
		t.Fatalf("stale %d %s", stale.status, staleBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "newer\n" {
		t.Fatalf("stale apply overwrote %q", got)
	}
	if got := readRepoFile(t, filepath.Join(repo, "added.txt")); got != "hello" {
		t.Fatalf("stale apply changed added file %q", got)
	}
}

func TestCreateRejectsNestedDataDir(t *testing.T) {
	st := testutil.Open(t)
	data := t.TempDir()
	repo := filepath.Join(data, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: data}, testToken).Handler())
	t.Cleanup(ts.Close)
	got := postRun(t, ts.URL, map[string]any{
		"goal": "nest", "repo_path": repo, "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"profile":     pythonProfile(),
	}, "idem-nest")
	if got.status != http.StatusBadRequest || !strings.Contains(got.body, "data directory") {
		t.Fatalf("create %d %s", got.status, got.body)
	}
}

func advance(t *testing.T, st *store.Store, current store.Run, states ...run.State) store.Run {
	t.Helper()
	for _, next := range states {
		var err error
		current, err = st.Transition(context.Background(), current.ID, current.StateVersion, next, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	return current
}

func writeRepoFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func readBody(t *testing.T, resp response) string {
	t.Helper()
	defer resp.body.Close()
	raw, err := io.ReadAll(resp.body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
