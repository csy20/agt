package sandbox

import (
	"context"
	"os"
	"os/exec"
)

// dockerProcessEnv is the environment of the docker CLI. It is not the
// container environment. Host secrets such as AGT_MODEL_API_KEY are left out.
func dockerProcessEnv() []string {
	keys := []string{
		"PATH", "PATHEXT", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC",
		"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "USER", "USERNAME",
		"TMP", "TEMP", "TMPDIR",
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME",
		"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
	}
	var out []string
	seen := map[string]struct{}{}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerProcessEnv()
	return cmd
}
