package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner executes the fixed test command inside a per-run container.
// The model never chooses the argv.
type Runner struct {
	Image      string
	ExtraLabel string

	mu    sync.Mutex
	execs int
}

func (r *Runner) ExecCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execs
}

// RunTest runs the fixture unittest inside the run container.
func (r *Runner) RunTest(ctx context.Context, worktree, runID string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return r.Exec(ctx, worktree, runID, []string{
		"timeout", "-s", "KILL", "60", "python", "-m", "unittest", "tests.test_retry",
	})
}

// Exec runs a fixed argv in the run container. Callers must not pass model text as a shell.
func (r *Runner) Exec(ctx context.Context, worktree, runID string, argv []string) (string, string, int, error) {
	if err := r.ensure(ctx, worktree, runID); err != nil {
		return "", "", 0, err
	}
	r.mu.Lock()
	r.execs++
	r.mu.Unlock()
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	args := []string{
		"exec",
		"--user", user,
		"-e", "HOME=/tmp",
		"-e", "PYTHONDONTWRITEBYTECODE=1",
		"-e", "PYTHONUNBUFFERED=1",
		"-e", "PYTHONPATH=/workspace",
		"-w", "/workspace",
		containerName(runID),
	}
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), stderr.String(), exit.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 0, err
}

// Destroy removes the run container if it exists.
func (r *Runner) Destroy(ctx context.Context, runID string) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", containerName(runID))
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("docker rm: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (r *Runner) ensure(ctx context.Context, worktree, runID string) error {
	if strings.Contains(worktree, ",") {
		return fmt.Errorf("worktree path %q cannot be mounted", worktree)
	}
	name := containerName(runID)
	status, err := inspectStatus(ctx, name)
	if err != nil {
		return r.start(ctx, name, worktree, runID)
	}
	if status != "running" {
		cmd := exec.CommandContext(ctx, "docker", "start", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker start: %w: %s", err, bytes.TrimSpace(out))
		}
	}
	return nil
}

func (r *Runner) start(ctx context.Context, name, worktree, runID string) error {
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
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
	if r.ExtraLabel != "" {
		args = append(args, "--label", r.ExtraLabel)
	}
	args = append(args, r.Image, "sleep", "infinity")
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func inspectStatus(ctx context.Context, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Status}}", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func containerName(runID string) string {
	var b strings.Builder
	b.WriteString("agt-")
	for _, r := range runID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
