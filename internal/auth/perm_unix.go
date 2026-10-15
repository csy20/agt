//go:build !windows

package auth

import (
	"fmt"
	"os"
)

func protectDir(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict data dir %s: %w", path, err)
	}
	return nil
}

func protectFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict auth.token: %w", err)
	}
	return nil
}

func filePrivate(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("auth.token is a symlink")
	}
	return info.Mode().Perm()&0o077 == 0, nil
}
