package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeRel accepts a repository-relative path and rejects escapes.
func SafeRel(path string) (string, error) {
	if path == "" || strings.Contains(path, "\x00") {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths are outside the worktree")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the worktree")
	}
	return clean, nil
}

// Within resolves rel inside root and refuses symlink escapes.
func Within(root, rel string) (string, error) {
	rel, err := SafeRel(rel)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, rel)
	if err := existingAncestorInside(rootResolved, full); err != nil {
		return "", err
	}
	return full, nil
}

// existingAncestorInside resolves the deepest path component that exists and
// rejects targets whose real path leaves root. Intermediate symlinks count.
func existingAncestorInside(rootAbs, full string) error {
	var missing []string
	cur := full
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		base := filepath.Base(cur)
		if base == ".." || base == "." || base == string(os.PathSeparator) {
			return fmt.Errorf("path escapes the worktree")
		}
		missing = append([]string{base}, missing...)
		parent := filepath.Dir(cur)
		if parent == cur {
			return fmt.Errorf("path escapes the worktree")
		}
		cur = parent
	}
	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return err
	}
	final := resolved
	for _, part := range missing {
		final = filepath.Join(final, part)
	}
	rel, err := filepath.Rel(rootAbs, final)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes the worktree through a symlink")
	}
	return nil
}
