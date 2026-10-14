package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Preflight checks the Docker daemon and the sandbox image. A failure is
// returned to the caller. Test execution does not fall back to the host.
func (r *Runner) Preflight(ctx context.Context) error {
	r.mu.Lock()
	if r.ready {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if err := preflight(ctx, r.Image); err != nil {
		return err
	}
	r.mu.Lock()
	r.ready = true
	r.mu.Unlock()
	return nil
}

func preflight(ctx context.Context, image string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("sandbox image is not configured. Set AGT_SANDBOX_IMAGE and build it with `make image`. agt does not run tests on the host")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker was not found on PATH. Install Docker and start its daemon. agt runs tests only inside a container and does not fall back to the host")
	}
	if out, err := dockerCommand(ctx, "info").CombinedOutput(); err != nil {
		return fmt.Errorf("docker is installed but the daemon is not reachable. Start Docker and retry. %s", clipOut(out))
	}
	if out, err := dockerCommand(ctx, "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox image %q is not available. Build it with `make image` or set AGT_SANDBOX_IMAGE to an image that exists. %s", image, clipOut(out))
	}
	return nil
}

func clipOut(out []byte) string {
	s := strings.TrimSpace(string(out))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}
