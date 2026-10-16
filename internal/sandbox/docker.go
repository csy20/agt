package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	// Namespace keeps this daemon's containers out of another daemon's sweep.
	// Empty means "default".
	Namespace string

	mu    sync.Mutex
	execs int
	ready bool
}

func (r *Runner) namespace() string {
	ns := strings.TrimSpace(r.Namespace)
	if ns == "" {
		ns = "default"
	}
	return sanitizeName(ns)
}

// NamespaceFor derives a stable container namespace from one data directory.
func NamespaceFor(dataDir string) string {
	sum := sha256.Sum256([]byte("agt-data\n" + dataDir))
	return "ns" + hex.EncodeToString(sum[:3])
}

// TestNamespace is unique to this test process.
func TestNamespace() string {
	return "t" + strconv.Itoa(os.Getpid())
}

// Owns reports whether labels belong to namespace and runID.
func Owns(namespace string, labels map[string]string, runID string) bool {
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}
	namespace = sanitizeName(namespace)
	if labels == nil {
		return false
	}
	return labels["agt-namespace"] == namespace && labels["agt-run"] == runID
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
	cmd := dockerCommand(ctx, containerExecArgs(containerName(r.namespace(), runID), user, spec.Workdir, spec.Env, spec.Argv)...)
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

// Destroy removes the run container when it belongs to this namespace.
// A missing container is success. An inspect failure does not remove anything.
func (r *Runner) Destroy(ctx context.Context, runID string) error {
	name := containerName(r.namespace(), runID)
	labels, missing, err := inspectLabels(ctx, name)
	if missing {
		return nil
	}
	if err != nil {
		return err
	}
	if !Owns(r.namespace(), labels, runID) {
		return fmt.Errorf("refusing to remove container %s: namespace or run does not match", name)
	}
	cmd := dockerCommand(ctx, "rm", "-f", name)
	out, err := cmd.CombinedOutput()
	if err != nil && !missingContainer(string(out)) {
		return fmt.Errorf("docker rm: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (r *Runner) ensure(ctx context.Context, worktree, runID string, spec ExecSpec) error {
	if err := validateMountSource(worktree); err != nil {
		return err
	}
	name := containerName(r.namespace(), runID)
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
	args, err := containerRunArgs(spec, name, worktree, runID, user, r.ExtraLabel, r.namespace())
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

func inspectLabels(ctx context.Context, name string) (map[string]string, bool, error) {
	cmd := dockerCommand(ctx, "inspect", "--format", "{{json .Config.Labels}}", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if missingContainer(string(out)) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("docker inspect: %w: %s", err, bytes.TrimSpace(out))
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(out), &labels); err != nil {
		return nil, false, fmt.Errorf("docker inspect labels: %w", err)
	}
	return labels, false, nil
}

func missingContainer(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no such object") || strings.Contains(lower, "no such container")
}

func containerName(namespace, runID string) string {
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}
	return "agt-" + sanitizeName(namespace) + "-" + sanitizeName(runID)
}

func sanitizeName(part string) string {
	var b strings.Builder
	for _, r := range part {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}
