package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Capture is the provenance of a snapshot. Revision is empty when the
// source is not a Git repository. ReadCapture never runs Git.
type Capture struct {
	Revision string
	Ref      string
}

// ReadCapture reads HEAD from a source repository. A missing or unreadable
// Git directory returns an empty capture. The caller still copies the
// working tree, including uncommitted files.
func ReadCapture(repo string) Capture {
	dir, ok := gitDir(repo)
	if !ok {
		return Capture{}
	}
	rev, ref := readHEAD(dir)
	return Capture{Revision: rev, Ref: ref}
}

// Overlap reports whether the data directory and the repository would nest.
// Materialize refuses that layout so a copy cannot recurse through runs/.
func Overlap(repo, dataDir string) error {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("repository and data directory are required")
	}
	repoPath, err := canonical(repo)
	if err != nil {
		return err
	}
	dataPath, err := canonical(dataDir)
	if err != nil {
		return err
	}
	if repoPath == dataPath {
		return fmt.Errorf("repository and data directory are the same")
	}
	if inside(repoPath, dataPath) {
		return fmt.Errorf("data directory is inside the repository")
	}
	if inside(dataPath, repoPath) {
		return fmt.Errorf("repository is inside the data directory")
	}
	return nil
}

func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func gitDir(repo string) (string, bool) {
	p := filepath.Join(repo, ".git")
	st, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if st.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", false
		}
		st, err = os.Stat(resolved)
		if err != nil {
			return "", false
		}
		p = resolved
	}
	if st.IsDir() {
		return p, true
	}
	if !st.Mode().IsRegular() {
		return "", false
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(body))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(line), prefix) {
		return "", false
	}
	dir := strings.TrimSpace(line[len(prefix):])
	if dir == "" {
		return "", false
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

func readHEAD(git string) (string, string) {
	body, err := os.ReadFile(filepath.Join(git, "HEAD"))
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(body))
	if line == "" {
		return "", ""
	}
	if !strings.HasPrefix(line, "ref:") {
		if isHexSHA(line) {
			return line, ""
		}
		return "", ""
	}
	ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
	if !safeGitRef(ref) {
		return "", ""
	}
	refPath := filepath.Join(git, filepath.FromSlash(ref))
	if !pathInside(git, refPath) {
		return "", ref
	}
	if raw, err := os.ReadFile(refPath); err == nil {
		sha := strings.TrimSpace(string(raw))
		if isHexSHA(sha) {
			return sha, ref
		}
	}
	if sha := packedRef(git, ref); sha != "" {
		return sha, ref
	}
	return "", ref
}

func safeGitRef(ref string) bool {
	if ref == "" || filepath.IsAbs(ref) || strings.Contains(ref, "\\") || strings.Contains(ref, "..") {
		return false
	}
	return strings.HasPrefix(ref, "refs/")
}

func pathInside(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func packedRef(git, want string) string {
	body, err := os.ReadFile(filepath.Join(git, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != want || !isHexSHA(fields[0]) {
			continue
		}
		return fields[0]
	}
	return ""
}

func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
