package sandbox

import (
	"fmt"
	"strings"
)

// ExecSpec is one sandbox command. The image, environment, and limits come
// from the stored profile. The model does not supply them.
type ExecSpec struct {
	Image   string
	Argv    []string
	Env     []string
	Workdir string
	Memory  string
	CPUs    string
	PIDs    int
}

func validateExecSpec(spec ExecSpec) error {
	if spec.Image == "" || strings.ContainsAny(spec.Image, " \t\r\n\x00,") {
		return fmt.Errorf("sandbox image is invalid")
	}
	switch spec.Memory {
	case "64m", "128m", "256m", "512m":
	default:
		return fmt.Errorf("sandbox memory is invalid")
	}
	if spec.CPUs != "1" {
		return fmt.Errorf("sandbox cpus must be 1")
	}
	if spec.PIDs < 32 || spec.PIDs > 256 {
		return fmt.Errorf("sandbox pids are invalid")
	}
	if spec.Workdir != "/workspace" && !strings.HasPrefix(spec.Workdir, "/workspace/") {
		return fmt.Errorf("sandbox workdir is outside /workspace")
	}
	if strings.Contains(spec.Workdir, "..") || strings.Contains(spec.Workdir, "\x00") {
		return fmt.Errorf("sandbox workdir is outside /workspace")
	}
	if len(spec.Argv) == 0 {
		return fmt.Errorf("sandbox argv is empty")
	}
	for _, arg := range spec.Argv {
		if strings.Contains(arg, "\x00") {
			return fmt.Errorf("sandbox argv contains NUL")
		}
	}
	for _, item := range spec.Env {
		if strings.ContainsAny(item, "\r\n\x00") || !strings.Contains(item, "=") {
			return fmt.Errorf("sandbox env is invalid")
		}
	}
	return nil
}
