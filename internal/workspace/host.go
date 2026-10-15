package workspace

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/csy20/agt/internal/profile"
)

// HostExec runs a profile argv on the host. Production does not call it.
// The server uses the sandbox runner. This exists so tests can exercise a
// profile when no sandbox image is configured.
func HostExec(ctx context.Context, root string, p profile.Profile) (string, string, int, error) {
	if err := p.Check(); err != nil {
		return "", "", 0, err
	}
	if len(p.Argv) == 0 {
		return "", "", 0, errors.New("empty argv")
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, p.Argv[0], p.Argv[1:]...)
	cmd.Dir = p.HostDir(root)
	cmd.Env = profile.HostEnv(p, root)
	var stdout, stderr strings.Builder
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
