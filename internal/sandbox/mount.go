package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// containerRunArgs is the docker run argv for one run container.
// The only bind mount is the run workspace at /workspace.
// Network is always none. Docker's default tmpfs is noexec, so /tmp must set
// exec: Go writes and runs the test binary there. The root filesystem stays
// read-only and nosuid stays set.
func containerRunArgs(spec ExecSpec, name, worktree, runID, user, extraLabel, namespace string) ([]string, error) {
	if err := validateMountSource(worktree); err != nil {
		return nil, err
	}
	if err := validateExecSpec(spec); err != nil {
		return nil, err
	}
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", "agt-run=" + runID,
		"--label", "agt-namespace=" + namespaceLabel(namespace),
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,size=128m",
		"--memory", spec.Memory,
		"--cpus", spec.CPUs,
		"--pids-limit", strconv.Itoa(spec.PIDs),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", user,
	}
	for _, item := range spec.Env {
		args = append(args, "-e", item)
	}
	args = append(args,
		"-w", "/workspace",
		"--mount", "type=bind,src="+worktree+",dst=/workspace",
	)
	if extraLabel != "" {
		args = append(args, "--label", extraLabel)
	}
	args = append(args, spec.Image, "sleep", "infinity")
	return args, nil
}

func namespaceLabel(namespace string) string {
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}
	return sanitizeName(namespace)
}

func containerExecArgs(name, user, workdir string, env, argv []string) []string {
	args := []string{
		"exec",
		"--user", user,
	}
	for _, item := range env {
		args = append(args, "-e", item)
	}
	args = append(args, "-w", workdir, name)
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
