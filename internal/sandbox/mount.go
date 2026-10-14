package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// containerRunArgs is the docker run argv for one run container.
// The only bind mount is the run workspace at /workspace.
func containerRunArgs(image, name, worktree, runID, user, extraLabel string) ([]string, error) {
	if err := validateMountSource(worktree); err != nil {
		return nil, err
	}
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", "agt-run=" + runID,
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--memory", "256m",
		"--cpus", "1",
		"--pids-limit", "64",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", user,
		"-e", "HOME=/tmp",
		"-e", "PYTHONDONTWRITEBYTECODE=1",
		"-w", "/workspace",
		"--mount", "type=bind,src=" + worktree + ",dst=/workspace",
	}
	if extraLabel != "" {
		args = append(args, "--label", extraLabel)
	}
	args = append(args, image, "sleep", "infinity")
	return args, nil
}

func containerExecArgs(name, user string, argv []string) []string {
	args := []string{
		"exec",
		"--user", user,
		"-e", "HOME=/tmp",
		"-e", "PYTHONDONTWRITEBYTECODE=1",
		"-e", "PYTHONUNBUFFERED=1",
		"-e", "PYTHONPATH=/workspace",
		"-w", "/workspace",
		name,
	}
	return append(args, argv...)
}

// validateMountSource refuses to mount the home directory itself, a credential
// directory, or the Docker socket. A workspace under the data directory is allowed.
func validateMountSource(worktree string) error {
	if strings.TrimSpace(worktree) == "" {
		return fmt.Errorf("worktree path is empty")
	}
	if strings.Contains(worktree, ",") || strings.Contains(worktree, "\x00") {
		return fmt.Errorf("worktree path %q cannot be mounted", worktree)
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) {
		return fmt.Errorf("worktree path %q is not absolute", worktree)
	}
	if isDockerSocket(abs) {
		return fmt.Errorf("refusing to mount the Docker socket")
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(abs, home) {
		return fmt.Errorf("refusing to mount the home directory")
	}
	if credentialPath(abs) {
		return fmt.Errorf("refusing to mount credential path %s", abs)
	}
	return nil
}

func credentialPath(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	for _, part := range strings.Split(slash, "/") {
		switch strings.ToLower(part) {
		case ".ssh", ".aws", ".azure", ".gnupg", ".kube", ".docker":
			return true
		}
	}
	return false
}

func isDockerSocket(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "docker.sock" || base == "docker_engine" {
		return true
	}
	slash := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(slash, "/docker.sock") || strings.HasSuffix(slash, "/pipe/docker_engine")
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
