//go:build !unix

package agent

import "os/exec"

func setProcGroup(cmd *exec.Cmd) {}

func killGroup(int) {}
