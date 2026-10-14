package tools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SafeRel accepts a repository-relative path and rejects escapes.
// Backslashes are separators. A colon is rejected so a Windows drive,
// a UNC path, or an alternate data stream cannot be addressed.
func SafeRel(p string) (string, error) {
	if p == "" || strings.Contains(p, "\x00") {
		return "", fmt.Errorf("path is empty")
	}
	if strings.Contains(p, ":") {
		return "", fmt.Errorf("path is outside the worktree")
	}
	slash := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(slash, "/") || filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute paths are outside the worktree")
	}
	clean := path.Clean(slash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path escapes the worktree")
	}
	return filepath.FromSlash(clean), nil
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
