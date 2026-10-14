package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const fileName = "auth.token"

// LoadOrCreate returns the daemon token for dataDir.
// AGT_TOKEN, when set, is stored in the data directory so a client that
// shares AGT_DATA_DIR and does not see the environment still presents it.
// On Unix, a token file that is group- or world-readable is replaced.
func LoadOrCreate(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("data dir: %w", err)
	}
	if err := protectDir(dataDir); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, fileName)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to use a symlinked auth.token")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if env := strings.TrimSpace(os.Getenv("AGT_TOKEN")); env != "" {
		if err := validateToken(env); err != nil {
			return "", fmt.Errorf("AGT_TOKEN: %w", err)
		}
		if err := writeToken(path, env); err != nil {
			return "", err
		}
		return env, nil
	}
	existing, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(existing))
		private, perr := filePrivate(path)
		if perr != nil {
			return "", perr
		}
		if private && validateToken(token) == nil {
			if err := protectFile(path); err != nil {
				return "", err
			}
			return token, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if err := writeToken(path, token); err != nil {
		return "", err
	}
	return token, nil
}

// FilePrivate reports whether auth.token exists, is not a symlink, and is
// private to the owner. It does not create a token and ignores AGT_TOKEN.
func FilePrivate(dataDir string) error {
	path := filepath.Join(dataDir, fileName)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to read a symlinked auth.token")
	}
	ok, err := filePrivate(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("auth.token is readable by someone other than the owner")
	}
	return nil
}

// Read returns AGT_TOKEN or the token file. It does not create a token.
func Read(dataDir string) (string, error) {
	if env := strings.TrimSpace(os.Getenv("AGT_TOKEN")); env != "" {
		if err := validateToken(env); err != nil {
			return "", fmt.Errorf("AGT_TOKEN: %w", err)
		}
		return env, nil
	}
	path := filepath.Join(dataDir, fileName)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read local token from %s: %w", dataDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to read a symlinked auth.token")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read local token from %s: %w", dataDir, err)
	}
	token := strings.TrimSpace(string(body))
	if err := validateToken(token); err != nil {
		return "", fmt.Errorf("local token in %s: %w", dataDir, err)
	}
	return token, nil
}

func newToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate local token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func validateToken(token string) error {
	if len(token) < 16 || len(token) > 256 {
		return fmt.Errorf("token length must be 16..256 characters")
	}
	for _, r := range token {
		if r <= ' ' || r >= 0x7f {
			return fmt.Errorf("token contains a character that cannot be placed in an Authorization header")
		}
	}
	return nil
}

func writeToken(path, token string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write auth.token through a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(token + "\n")
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := protectFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return protectFile(path)
}
