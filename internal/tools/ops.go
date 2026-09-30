package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxReadBytes = 1 << 20

// Search returns literal matches as path:line:text, capped at 50 hits.
func Search(root, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("empty query")
	}
	var b strings.Builder
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > maxReadBytes {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, query) {
				fmt.Fprintf(&b, "%s:%d:%s\n", rel, i+1, clip(line, 200))
				n++
				if n >= 50 {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if b.Len() == 0 {
		return "(no matches)\n", nil
	}
	return b.String(), nil
}

// ReadRange returns 1-based inclusive lines.
func ReadRange(root, rel string, start, end int) (string, error) {
	full, err := Within(root, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if len(data) > maxReadBytes {
		return "", fmt.Errorf("file exceeds read limit")
	}
	lines := strings.Split(string(data), "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", fmt.Errorf("start line past end of file")
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return b.String(), nil
}

// Patch replaces old with new exactly once. A second call with the same
// arguments reports already applied and does not write.
func Patch(root, rel, old, new string) (string, error) {
	full, err := Within(root, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	cur := string(data)
	switch {
	case strings.Contains(cur, old):
		if strings.Count(cur, old) != 1 {
			return "", fmt.Errorf("old text is not unique")
		}
		updated := strings.Replace(cur, old, new, 1)
		if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
			return "", err
		}
		return "patched", nil
	case old != new && strings.Contains(cur, new):
		return "already applied", nil
	default:
		return "", fmt.Errorf("old text not found")
	}
}

// Diff returns a unified diff of work against the immutable base snapshot.
func Diff(base, work string) (string, error) {
	files, err := unionFiles(base, work)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	for _, rel := range files {
		bp := filepath.Join(base, rel)
		wp := filepath.Join(work, rel)
		args := []string{"-u", "--label", "a/" + rel, "--label", "b/" + rel}
		_, baseErr := os.Stat(bp)
		_, workErr := os.Stat(wp)
		switch {
		case baseErr != nil && workErr == nil:
			args = append(args, "/dev/null", wp)
		case baseErr == nil && workErr != nil:
			args = append(args, bp, "/dev/null")
		default:
			args = append(args, bp, wp)
		}
		cmd := exec.Command("diff", args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			continue
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			b.Write(out)
			continue
		}
		if len(out) > 0 {
			b.Write(out)
		}
	}
	if b.Len() == 0 {
		return "(no changes)\n", nil
	}
	if b.Len() > 64*1024 {
		return b.String()[:64*1024] + "\n(diff truncated)\n", nil
	}
	return b.String(), nil
}

func unionFiles(base, work string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, root := range []string{base, work} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			seen[rel] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
