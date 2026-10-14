package workspace

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/csy20/agt/internal/tools"
)

// TestFunc runs the fixed repository test command. Production points it at a
// sandbox; tests may point it at a local interpreter.
type TestFunc func(ctx context.Context, root string) (stdout, stderr string, code int, err error)

// Handle is the workspace the orchestrator is allowed to touch.
type Handle interface {
	Search(ctx context.Context, query string) (string, int, error)
	Read(ctx context.Context, path string, start, end int) (string, int, error)
	Diff(ctx context.Context) (string, int, error)
	Patch(ctx context.Context, path, old, new string) (string, int, error)
	Write(ctx context.Context, args tools.WriteArgs) (string, int, error)
	Delete(ctx context.Context, args tools.DeleteArgs) (string, int, error)
	Test(ctx context.Context) (string, int, error)
	Calls() []string
	Close(ctx context.Context) error
}

// Dir performs file tools on a copied worktree. Test execution is injected so
// model-requested process execution is not the control-plane shell.
type Dir struct {
	Work    string
	Base    string
	TestFn  TestFunc
	CloseFn func(context.Context) error

	mu    sync.Mutex
	calls []string
}

func NewDir(work, base string, testFn TestFunc) *Dir {
	return &Dir{Work: work, Base: base, TestFn: testFn}
}

func (d *Dir) note(tool string) {
	d.mu.Lock()
	d.calls = append(d.calls, tool)
	d.mu.Unlock()
}

func (d *Dir) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.calls))
	copy(out, d.calls)
	return out
}

func (d *Dir) Search(_ context.Context, query string) (string, int, error) {
	d.note("search_code")
	out, err := tools.Search(d.Work, query)
	if err != nil {
		return err.Error(), 2, nil
	}
	return out, 0, nil
}

func (d *Dir) Read(_ context.Context, path string, start, end int) (string, int, error) {
	d.note("read_file_range")
	out, err := tools.ReadRange(d.Work, path, start, end)
	if err != nil {
		return err.Error(), 2, nil
	}
	return out, 0, nil
}

func (d *Dir) Diff(context.Context) (string, int, error) {
	d.note("git_diff")
	out, err := tools.Diff(d.Base, d.Work)
	if err != nil {
		return "", 2, err
	}
	return out, 0, nil
}

func (d *Dir) Patch(_ context.Context, path, old, new string) (string, int, error) {
	d.note("apply_patch")
	out, err := tools.Patch(d.Work, path, old, new)
	if err != nil {
		return err.Error(), 2, nil
	}
	return out, 0, nil
}

func (d *Dir) Write(_ context.Context, args tools.WriteArgs) (string, int, error) {
	d.note("write_file")
	out, err := tools.WriteFile(d.Work, args)
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return out, 2, nil
	}
	return out, 0, nil
}

func (d *Dir) Delete(_ context.Context, args tools.DeleteArgs) (string, int, error) {
	d.note("delete_file")
	out, err := tools.DeleteFile(d.Work, args)
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return out, 2, nil
	}
	return out, 0, nil
}

func (d *Dir) Test(ctx context.Context) (string, int, error) {
	d.note("run_test")
	if d.TestFn == nil {
		return "", 0, fmt.Errorf("no test runner configured")
	}
	stdout, stderr, code, err := d.TestFn(ctx, d.Work)
	if err != nil {
		return "", code, err
	}
	out := strings.TrimSpace(stdout + "\n" + stderr)
	if out == "" {
		out = "(no output)"
	}
	return out, code, nil
}

func (d *Dir) Close(ctx context.Context) error {
	if d.CloseFn != nil {
		return d.CloseFn(ctx)
	}
	return nil
}

// MaxCopyFileBytes is the largest regular file Materialize will copy.
// Larger files fail the copy instead of being dropped silently.
const MaxCopyFileBytes = 8 << 20

// Materialize copies repo into dataDir/runs/<id>/{work,base}.
// Symlinks are refused. Executable bits are preserved.
// Dotfiles and dot-directories are included except the directory names
// reported by tools.SkipDir (.git, node_modules, build outputs, and similar)
// and secret names such as .env, .ssh, and private keys. .env.example is copied.
func Materialize(repo, dataDir, runID string) (work, base string, err error) {
	root := filepath.Join(dataDir, "runs", runID)
	if err := os.RemoveAll(root); err != nil {
		return "", "", err
	}
	work = filepath.Join(root, "work")
	base = filepath.Join(root, "base")
	if err := copyTree(repo, work); err != nil {
		return "", "", err
	}
	if err := copyTree(repo, base); err != nil {
		return "", "", err
	}
	return work, base, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("copy escapes source")
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && (tools.SkipDir(d.Name()) || tools.SecretDir(d.Name())) {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() || tools.SecretFile(d.Name()) {
			return nil
		}
		if info.Size() > MaxCopyFileBytes {
			return fmt.Errorf("file %s is %d bytes, over the %d byte copy limit", rel, info.Size(), MaxCopyFileBytes)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}
