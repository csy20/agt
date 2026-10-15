package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/csy20/agt/internal/profile"
)

// Runner executes the run's stored profile inside a per-run container.
// The model never chooses the argv, the image, or the network policy.
type Runner struct {
	Images     profile.Images
	ExtraLabel string

	mu    sync.Mutex
	execs int
	ready bool
}

func (r *Runner) ExecCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execs
}

// RunProfile runs the stored profile. A stored image that no longer matches
// the daemon configuration fails the run instead of selecting another image.
func (r *Runner) RunProfile(ctx context.Context, worktree, runID string, p profile.Profile) (string, string, int, error) {
	if err := p.Check(); err != nil {
		return "", "", 0, err
	}
	want, err := r.Images.For(p.Name)
	if err != nil {
		return "", "", 0, err
	}
	if p.Image != want {
		return "", "", 0, fmt.Errorf("profile %s image %q does not match the configured image %q", p.Name, p.Image, want)
	}
	spec := ExecSpec{
		Image:   p.Image,
		Argv:    p.ContainerArgv(),
		Env:     append([]string(nil), p.Env...),
		Workdir: p.ContainerWorkdir(),
		Memory:  p.Memory,
		CPUs:    p.CPUs,
		PIDs:    p.PIDs,
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSec+15)*time.Second)
	defer cancel()
	return r.Exec(ctx, worktree, runID, spec)
}

// Exec runs a fixed argv in the run container. Callers must not pass model text as a shell.
func (r *Runner) Exec(ctx context.Context, worktree, runID string, spec ExecSpec) (string, string, int, error) {
	if err := r.Preflight(ctx); err != nil {
		return "", "", 0, err
	}
	if err := validateExecSpec(spec); err != nil {
		return "", "", 0, err
	}
	if err := r.ensure(ctx, worktree, runID, spec); err != nil {
		return "", "", 0, err
	}
	r.mu.Lock()
	r.execs++
	r.mu.Unlock()
	user, err := currentDockerUser()
	if err != nil {
		return "", "", 0, err
	}
	cmd := dockerCommand(ctx, containerExecArgs(containerName(runID), user, spec.Workdir, spec.Env, spec.Argv)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
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
	cmd := dockerCommand(ctx, "rm", "-f", containerName(runID))
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("docker rm: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (r *Runner) ensure(ctx context.Context, worktree, runID string, spec ExecSpec) error {
	if err := validateMountSource(worktree); err != nil {
		return err
	}
	name := containerName(runID)
	status, err := inspectStatus(ctx, name)
	if err != nil {
		return r.start(ctx, name, worktree, runID, spec)
	}
	got, err := inspectImage(ctx, name)
	if err != nil {
		return err
	}
	if got != spec.Image {
		return fmt.Errorf("run container image %q does not match profile image %q", got, spec.Image)
	}
	if status != "running" {
		cmd := dockerCommand(ctx, "start", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker start: %w: %s", err, bytes.TrimSpace(out))
		}
	}
	return nil
}

func (r *Runner) start(ctx context.Context, name, worktree, runID string, spec ExecSpec) error {
	user, err := currentDockerUser()
	if err != nil {
		return err
	}
	args, err := containerRunArgs(spec, name, worktree, runID, user, r.ExtraLabel)
	if err != nil {
		return err
	}
	cmd := dockerCommand(ctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func inspectStatus(ctx context.Context, name string) (string, error) {
	cmd := dockerCommand(ctx, "inspect", "--format", "{{.State.Status}}", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func inspectImage(ctx context.Context, name string) (string, error) {
	cmd := dockerCommand(ctx, "inspect", "--format", "{{.Config.Image}}", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker inspect image: %w: %s", err, bytes.TrimSpace(out))
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
