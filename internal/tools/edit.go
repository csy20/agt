package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const MaxWriteBytes = 200_000

// ErrConflict means the file hash does not match the command precondition.
// The file is left unchanged.
var ErrConflict = errors.New("conflict")

// SHA256 is the lowercase hex digest stored with file commands.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// WriteFile creates or replaces one text file. An empty before hash means the
// file must not exist. When the current bytes already hash to the requested
// content, the call is already applied and does not write again.
func WriteFile(root string, args WriteArgs) (string, error) {
	if strings.Contains(args.Content, "\x00") {
		return "", fmt.Errorf("content must be text")
	}
	if len(args.Content) > MaxWriteBytes {
		return "", fmt.Errorf("content must be under 200KB")
	}
	if !validSHA(args.BeforeSHA256, false) {
		return "", fmt.Errorf("before_sha256 must be empty or 64 lowercase hex characters")
	}
	clean, full, err := editable(root, args.Path)
	if err != nil {
		return "", err
	}
	data, info, err := readRegular(full)
	if err != nil {
		return "", err
	}
	if info != nil && info.IsDir() {
		return "", fmt.Errorf("refusing to edit a directory")
	}
	want := SHA256([]byte(args.Content))
	current := ""
	if data != nil {
		current = SHA256(data)
	}
	res := FileResult{Path: slash(clean), BeforeSHA256: args.BeforeSHA256, AfterSHA256: want}
	if data != nil && current == want {
		res.Action = "already_applied"
		res.BeforeSHA256 = current
		res.AfterSHA256 = current
		return mustResult(res)
	}
	if data == nil {
		if args.BeforeSHA256 != "" {
			res.Action = "conflict"
			res.AfterSHA256 = ""
			body, _ := mustResult(res)
			return body, fmt.Errorf("%w: file is absent", ErrConflict)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := atomicWrite(full, []byte(args.Content), 0o644); err != nil {
			return "", err
		}
		res.Action = "created"
		return mustResult(res)
	}
	if args.BeforeSHA256 != current {
		res.Action = "conflict"
		res.BeforeSHA256 = current
		res.AfterSHA256 = current
		body, _ := mustResult(res)
		return body, fmt.Errorf("%w: before_sha256 does not match", ErrConflict)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := atomicWrite(full, []byte(args.Content), mode); err != nil {
		return "", err
	}
	res.Action = "written"
	return mustResult(res)
}

// DeleteFile removes one regular file whose bytes hash to beforeSHA256.
// A missing file is already applied. A directory is refused.
func DeleteFile(root string, args DeleteArgs) (string, error) {
	if !validSHA(args.BeforeSHA256, true) {
		return "", fmt.Errorf("before_sha256 must be 64 lowercase hex characters")
	}
	clean, full, err := editable(root, args.Path)
	if err != nil {
		return "", err
	}
	data, info, err := readRegular(full)
	if err != nil {
		return "", err
	}
	res := FileResult{Path: slash(clean), BeforeSHA256: args.BeforeSHA256}
	if info != nil && info.IsDir() {
		return "", fmt.Errorf("refusing to delete a directory")
	}
	if data == nil {
		res.Action = "already_applied"
		return mustResult(res)
	}
	current := SHA256(data)
	if current != args.BeforeSHA256 {
		res.Action = "conflict"
		res.BeforeSHA256 = current
		res.AfterSHA256 = current
		body, _ := mustResult(res)
		return body, fmt.Errorf("%w: before_sha256 does not match", ErrConflict)
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	res.Action = "deleted"
	res.AfterSHA256 = ""
	return mustResult(res)
}

func editable(root, rel string) (string, string, error) {
	clean, err := SafeRel(rel)
	if err != nil {
		return "", "", err
	}
	if SecretRel(clean) {
		return "", "", fmt.Errorf("secret paths are not available to the agent")
	}
	full, err := Within(root, clean)
	if err != nil {
		return "", "", err
	}
	return clean, full, nil
}

// readRegular returns nil data when the path is absent. A directory is returned
// with no bytes so the caller can refuse it. A symlink is an error so an edit
// cannot follow it out of the worktree.
func readRegular(full string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("refusing to edit a symlink")
	}
	if info.IsDir() {
		return nil, info, nil
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refusing to edit a non-regular file")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".agt-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := replaceFile(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func replaceFile(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
		return err
	}
	return os.Rename(tmp, dst)
}

func validSHA(s string, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func slash(rel string) string {
	return filepath.ToSlash(rel)
}

func mustResult(res FileResult) (string, error) {
	raw, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
