package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/profile"
)

func TestContainerRunMountsOnlyTheWorktree(t *testing.T) {
	work := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	spec := ExecSpec{
		Image:   "agt-sandbox:local",
		Argv:    []string{"python", "-m", "unittest"},
		Env:     profile.EnvFor(profile.NamePython),
		Workdir: "/workspace",
		Memory:  "256m",
		CPUs:    "1",
		PIDs:    64,
	}
	args, err := containerRunArgs(spec, "agt-run", work, "run_1", "1000:1000", "")
	if err != nil {
		t.Fatal(err)
	}
	mounts := flagValues(args, "--mount")
	if len(mounts) != 1 || mounts[0] != "type=bind,src="+work+",dst=/workspace" {
		t.Fatalf("mounts %q", mounts)
	}
	if vols := flagValues(args, "-v"); len(vols) != 0 {
		t.Fatalf("volume flags %q", vols)
	}
	if vols := flagValues(args, "--volume"); len(vols) != 0 {
		t.Fatalf("volume flags %q", vols)
	}
	joined := strings.Join(args, "\n")
	for _, banned := range []string{"docker.sock", "docker_engine", filepath.Clean(home) + "\n", "/.ssh", "/.aws", "/.kube"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("argv contains %q\n%s", banned, joined)
		}
	}
	assertFlag(t, args, "--network", "none")
	assertFlag(t, args, "--tmpfs", "/tmp:rw,exec,nosuid,size=128m")
	assertFlag(t, args, "--cap-drop", "ALL")
	assertFlag(t, args, "--security-opt", "no-new-privileges")
	assertFlag(t, args, "--memory", "256m")
	assertFlag(t, args, "--cpus", "1")
	assertFlag(t, args, "--pids-limit", "64")
	assertFlag(t, args, "--user", "1000:1000")
	if !containsArg(args, "--read-only") {
		t.Fatal("missing --read-only")
	}
	envs := flagValues(args, "-e")
	if strings.Join(envs, ",") != strings.Join(profile.EnvFor(profile.NamePython), ",") {
		t.Fatalf("run env %q", envs)
	}
	for _, banned := range []string{"AGT_", "AWS_", "DATABASE_URL"} {
		if strings.Contains(strings.Join(envs, "\n"), banned) {
			t.Fatalf("env contains %s", banned)
		}
	}
	execArgs := containerExecArgs("agt-run", "1000:1000", spec.Workdir, spec.Env, spec.Argv)
	execEnv := flagValues(execArgs, "-e")
	if strings.Join(execEnv, ",") != strings.Join(spec.Env, ",") {
		t.Fatalf("exec env %q", execEnv)
	}
	if flagValues(execArgs, "-w")[0] != "/workspace" {
		t.Fatalf("workdir %q", flagValues(execArgs, "-w"))
	}
}

func TestMountSourceRejectsHomeCredentialsAndSocket(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		t.TempDir(),
		filepath.Join(home, ".local", "share", "agt", "runs", "run_1", "work"),
	}
	for _, path := range allowed {
		if err := validateMountSource(path); err != nil {
			t.Errorf("allow %s: %v", path, err)
		}
	}
	rejected := []string{
		home,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(t.TempDir(), ".kube"),
		"/var/run/docker.sock",
		"/run/docker.sock",
	}
	for _, path := range rejected {
		if err := validateMountSource(path); err == nil {
			t.Errorf("mounted %s", path)
		}
	}
}

func TestDockerProcessEnvDropsHostSecrets(t *testing.T) {
	t.Setenv("AGT_MODEL_API_KEY", "sk-host-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-host-secret")
	t.Setenv("AGT_DATABASE_URL", "postgres://agt:secret@127.0.0.1/agt")
	cmd := dockerCommand(context.Background(), "info")
	for _, entry := range cmd.Env {
		for _, secret := range []string{"sk-host-secret", "aws-host-secret", "postgres://agt:secret"} {
			if strings.Contains(entry, secret) {
				t.Fatalf("docker CLI env contains %s", entry)
			}
		}
	}
	if !hasKey(cmd.Env, "PATH") {
		t.Fatal("PATH was dropped")
	}
}

func flagValues(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			out = append(out, args[i+1])
		}
	}
	return out
}

func assertFlag(t *testing.T, args []string, name, want string) {
	t.Helper()
	got := flagValues(args, name)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("%s = %q, want %s", name, got, want)
	}
}

func containsArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

func hasKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
