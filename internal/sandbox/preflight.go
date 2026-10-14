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
	images := r.Images.List()
	if len(images) == 0 {
		return fmt.Errorf("sandbox image is not configured. Set AGT_IMAGE_PYTHON, AGT_IMAGE_GO, and AGT_IMAGE_NODE. AGT_SANDBOX_IMAGE still selects the Python image. Build them with `make image`. agt does not run tests on the host")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker was not found on PATH. Install Docker and start its daemon. agt runs tests only inside a container and does not fall back to the host")
	}
	if out, err := dockerCommand(ctx, "info").CombinedOutput(); err != nil {
		return fmt.Errorf("docker is installed but the daemon is not reachable. Start Docker and retry. %s", clipOut(out))
	}
	for _, image := range images {
		if out, err := dockerCommand(ctx, "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput(); err != nil {
			return fmt.Errorf("sandbox image %q is not available. Build it with `make image`. %s", image, clipOut(out))
		}
	}
	r.mu.Lock()
	r.ready = true
	r.mu.Unlock()
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
