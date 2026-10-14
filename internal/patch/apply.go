package patch

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/csy20/agt/internal/tools"
)

var (
	// ErrDigest means the artifact does not match its digest or contract.
	ErrDigest = errors.New("patch digest does not match")
	// ErrConflict means a touched file no longer matches the snapshot.
	// Apply writes nothing when this is returned from the preflight.
	ErrConflict = errors.New("target does not match the snapshot")
	// ErrEscape means a path leaves the target repository.
	ErrEscape = errors.New("path escapes the repository")
)

// beforeMutate runs after the rollback record is durable and before each
// target write. Tests set it to stop an apply partway through.
var beforeMutate = func(string) error { return nil }

// Apply writes art into repo. It rejects a digest mismatch, a path escape,
// and any touched file whose current bytes or mode differ from the snapshot.
// Untouched files, including unrelated dirty files, are left alone.
// rollbackDir receives copies of the previous bytes and a manifest before
// the first target write. A failure after that point restores the files
// this function already changed.
func Apply(repo string, art Artifact, rollbackDir string) error {
	if err := art.check(); err != nil {
		return err
	}
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	info, err := os.Stat(repoAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("repository is not a directory")
	}
	plan, err := preflight(repoAbs, art.Files)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}
	if err := writeRollback(rollbackDir, repoAbs, art.Digest, plan); err != nil {
		return err
	}
	var done []int
	var createdDirs []string
	for i := range plan {
		if err := beforeMutate(plan[i].full); err != nil {
			return restoreAfter(plan, done, createdDirs, err)
		}
		dirs, err := commitOne(repoAbs, &plan[i])
		createdDirs = append(createdDirs, dirs...)
		if err != nil {
			return restoreAfter(plan, done, createdDirs, err)
		}
		done = append(done, i)
	}
	return nil
}

func (a Artifact) check() error {
	if a.Contract != Contract || a.Strategy != Strategy {
		return fmt.Errorf("%w: contract %s", ErrDigest, a.Contract)
	}
	if Digest(a) != a.Digest || a.Digest == "" {
		return ErrDigest
	}
	seen := map[string]struct{}{}
	for _, f := range a.Files {
		if _, ok := seen[f.Path]; ok {
			return fmt.Errorf("%w: duplicate %s", ErrDigest, f.Path)
		}
		seen[f.Path] = struct{}{}
		if _, err := f.afterBytes(); err != nil {
			return fmt.Errorf("%w: %s", ErrDigest, err.Error())
		}
		switch f.Op {
		case OpAdd:
			if f.BeforeMode != "" || f.BeforeSHA256 != "" || f.AfterMode == "" || f.AfterSHA256 == "" {
				return fmt.Errorf("%w: add %s", ErrDigest, f.Path)
			}
		case OpDelete:
			if f.AfterMode != "" || f.AfterSHA256 != "" || f.ContentBase64 != "" || f.BeforeMode == "" || f.BeforeSHA256 == "" {
				return fmt.Errorf("%w: delete %s", ErrDigest, f.Path)
			}
		case OpModify:
			if f.BeforeMode == "" || f.AfterMode == "" || f.BeforeSHA256 == "" || f.AfterSHA256 == "" {
				return fmt.Errorf("%w: modify %s", ErrDigest, f.Path)
			}
		default:
			return fmt.Errorf("%w: op %s", ErrDigest, f.Op)
		}
	}
	return nil
}

func (f File) afterBytes() ([]byte, error) {
	if f.Op == OpDelete {
		if f.ContentBase64 != "" {
			return nil, fmt.Errorf("delete %s has content", f.Path)
		}
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(f.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("content %s: %w", f.Path, err)
	}
	if hashBytes(raw) != f.AfterSHA256 {
		return nil, fmt.Errorf("content hash mismatch for %s", f.Path)
	}
	return raw, nil
}

type planned struct {
	file      File
	rel       string
	full      string
	after     []byte
	mode      os.FileMode
	existed   bool
	prior     []byte
	priorMode os.FileMode
}

func preflight(repo string, files []File) ([]planned, error) {
	plan := make([]planned, 0, len(files))
	for _, f := range files {
		rel, err := tools.SafeRel(f.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrEscape, f.Path)
		}
		full, err := tools.Within(repo, f.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrEscape, f.Path)
		}
		exists, mode, sum, body, err := readRegular(full)
		if err != nil {
			return nil, err
		}
		switch f.Op {
		case OpAdd:
			if exists {
				return nil, fmt.Errorf("%w: %s already exists", ErrConflict, f.Path)
			}
		case OpModify, OpDelete:
			if !exists || sum != f.BeforeSHA256 || mode != f.BeforeMode {
				return nil, fmt.Errorf("%w: %s", ErrConflict, f.Path)
			}
		default:
			return nil, fmt.Errorf("%w: op %s", ErrDigest, f.Op)
		}
		after, err := f.afterBytes()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrDigest, err.Error())
		}
		var perm os.FileMode
		if f.Op != OpDelete {
			perm, err = parseMode(f.AfterMode)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", ErrDigest, err.Error())
			}
		}
		item := planned{file: f, rel: rel, full: full, after: after, mode: perm, existed: exists}
		if exists {
			item.prior = append([]byte(nil), body...)
			parsed, err := parseMode(mode)
			if err != nil {
				return nil, err
			}
			item.priorMode = parsed
		}
		plan = append(plan, item)
	}
	return plan, nil
}

func readRegular(full string) (exists bool, mode, sum string, body []byte, err error) {
	st, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return false, "", "", nil, nil
	}
	if err != nil {
		return false, "", "", nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return false, "", "", nil, fmt.Errorf("%w: %s is not a regular file", ErrEscape, full)
	}
	body, err = os.ReadFile(full)
	if err != nil {
		return false, "", "", nil, err
	}
	return true, formatMode(st.Mode()), hashBytes(body), body, nil
}

func parseMode(s string) (os.FileMode, error) {
	if s == "" || strings.ContainsAny(s, " \t") {
		return 0, fmt.Errorf("mode %q", s)
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("mode %q", s)
	}
	return os.FileMode(n), nil
}

type rollbackFile struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Existed bool   `json:"existed"`
	Mode    string `json:"mode,omitempty"`
	Blob    string `json:"blob,omitempty"`
}

type rollbackManifest struct {
	Contract string         `json:"contract"`
	Repo     string         `json:"repo"`
	Digest   string         `json:"digest"`
	Files    []rollbackFile `json:"files"`
}

func writeRollback(dir, repo, digest string, plan []planned) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	manifest := rollbackManifest{Contract: Contract, Repo: repo, Digest: digest}
	for i, item := range plan {
		rec := rollbackFile{Path: item.file.Path, Op: item.file.Op, Existed: item.existed}
		if item.existed {
			name := fmt.Sprintf("blob-%04d", i)
			if err := os.WriteFile(filepath.Join(dir, name), item.prior, 0o600); err != nil {
				return err
			}
			rec.Mode = formatMode(item.priorMode)
			rec.Blob = name
		}
		manifest.Files = append(manifest.Files, rec)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600)
}

func commitOne(repo string, item *planned) ([]string, error) {
	if item.file.Op == OpDelete {
		if err := os.Remove(item.full); err != nil {
			return nil, err
		}
		return nil, nil
	}
	parent := filepath.Dir(item.full)
	created, err := ensureDirs(repo, parent)
	if err != nil {
		return created, err
	}
	tmp, err := os.CreateTemp(parent, ".agt-apply-*")
	if err != nil {
		return created, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(item.after); err != nil {
		tmp.Close()
		return created, err
	}
	if err := tmp.Chmod(item.mode); err != nil {
		tmp.Close()
		return created, err
	}
	if err := tmp.Close(); err != nil {
		return created, err
	}
	if err := os.Rename(tmpName, item.full); err != nil {
		return created, err
	}
	ok = true
	return created, nil
}

func ensureDirs(repo, dir string) ([]string, error) {
	rel, err := filepath.Rel(repo, dir)
	if err != nil {
		return nil, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: %s", ErrEscape, dir)
	}
	var created []string
	cur := repo
	if rel == "." {
		return nil, nil
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			if err := os.Mkdir(cur, 0o755); err != nil {
				return created, err
			}
			created = append(created, cur)
			continue
		}
		if err != nil {
			return created, err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return created, fmt.Errorf("%w: %s is not a directory", ErrEscape, cur)
		}
	}
	return created, nil
}

func restoreAfter(plan []planned, done []int, created []string, cause error) error {
	var problems []string
	for i := len(done) - 1; i >= 0; i-- {
		if err := restoreOne(plan[done[i]]); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for i := len(created) - 1; i >= 0; i-- {
		_ = os.Remove(created[i])
	}
	if len(problems) > 0 {
		return fmt.Errorf("apply stopped: %v; rollback incomplete: %s", cause, strings.Join(problems, "; "))
	}
	return fmt.Errorf("apply stopped: %v; restored %d file(s)", cause, len(done))
}

func restoreOne(item planned) error {
	if !item.existed {
		if err := os.Remove(item.full); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	parent := filepath.Dir(item.full)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".agt-rollback-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(item.prior); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(item.priorMode); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, item.full); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
