package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffReportsChangesWhenHostDiffIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("diff"); err == nil {
		t.Fatal("host diff is still on PATH")
	}
	base := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "a.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The base file lives at a different relative path so this is an addition
	// plus a deletion. Also cover an in-place edit below.
	if err := os.WriteFile(filepath.Join(base, "keep.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Diff(base, work)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "(no changes)") {
		t.Fatalf("missing host diff was reported as no changes:\n%s", out)
	}
	if !strings.Contains(out, "--- a/keep.txt\n+++ b/keep.txt\n") {
		t.Fatalf("edit labels:\n%s", out)
	}
	if !strings.Contains(out, "-one\n+two\n") {
		t.Fatalf("edit body:\n%s", out)
	}
	if strings.Contains(out, `\`) {
		t.Fatalf("repository label used a backslash:\n%s", out)
	}
}

func TestDiffContextMatchesGNUDiff(t *testing.T) {
	if _, err := exec.LookPath("diff"); err != nil {
		t.Fatal("diff is required to lock the unified format")
	}
	var oldLines, newLines []string
	for i := 0; i < 20; i++ {
		oldLines = append(oldLines, fmt.Sprintf("l%d", i))
		newLines = append(newLines, fmt.Sprintf("l%d", i))
	}
	newLines[10] = "changed"
	oldText := strings.Join(oldLines, "\n") + "\n"
	newText := strings.Join(newLines, "\n") + "\n"
	base := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte(oldText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(newText), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Diff(base, work)
	if err != nil {
		t.Fatal(err)
	}
	want := gnuDiff(t, filepath.Join(base, "f.txt"), filepath.Join(work, "f.txt"), "f.txt")
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRepoLabelUsesForwardSlashes(t *testing.T) {
	if got := repoLabel(`pkg\sub\file.go`); got != "pkg/sub/file.go" {
		t.Fatalf("label %q", got)
	}
}

func TestDiffMatchesGNUDiff(t *testing.T) {
	if _, err := exec.LookPath("diff"); err != nil {
		t.Fatal("diff is required to lock the unified format")
	}
	cases := []struct {
		name string
		base []byte
		work []byte
	}{
		{name: "edit", base: []byte("a\nb\nc\n"), work: []byte("a\nB\nc\n")},
		{name: "add", work: []byte("one\ntwo\n")},
		{name: "del", base: []byte("a\nb\nc\n")},
		{name: "nonew", base: []byte("a\nb\nc\n"), work: []byte("one\ntwo")},
		{name: "bothnonew", base: []byte("one\ntwo"), work: []byte("one\nTWO")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			work := t.TempDir()
			rel := filepath.Join("sub", "dir", "f.txt")
			label := "sub/dir/f.txt"
			var basePath, workPath string
			if tc.base != nil {
				basePath = filepath.Join(base, rel)
				if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(basePath, tc.base, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.work != nil {
				workPath = filepath.Join(work, rel)
				if err := os.MkdirAll(filepath.Dir(workPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(workPath, tc.work, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Diff(base, work)
			if err != nil {
				t.Fatal(err)
			}
			want := gnuDiff(t, basePath, workPath, label)
			if got != want {
				t.Fatalf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func gnuDiff(t *testing.T, basePath, workPath, label string) string {
	t.Helper()
	oldLabel := "a/" + label
	newLabel := "b/" + label
	oldPath := basePath
	newPath := workPath
	if basePath == "" {
		oldLabel = "/dev/null"
		oldPath = "/dev/null"
	}
	if workPath == "" {
		newLabel = "/dev/null"
		newPath = "/dev/null"
	}
	cmd := exec.Command("diff", "-u", "--label", oldLabel, "--label", newLabel, oldPath, newPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return string(out)
	}
	t.Fatalf("gnu diff: %v\n%s", err, out)
	return ""
}
