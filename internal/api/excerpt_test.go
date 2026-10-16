package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexExcerptsUseOnlyRegularSnapshotFiles(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "payments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "payments", "processor.py"), []byte("def charge():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "payments", "processor.py"), filepath.Join(work, "tests", "test_retry.py")); err != nil {
		t.Fatal(err)
	}
	got := codexExcerpts(work)
	if len(got) != 1 || got[0] != "payments/processor.py" {
		t.Fatalf("excerpts %v", got)
	}
}
