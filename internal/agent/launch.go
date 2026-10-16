package agent

import "fmt"

// LaunchRequest is the call this package refuses.
// Authorized is not enough: a credential and a working directory do not mediate
// the child CLI. WorkDir is not used as a sandbox.
type LaunchRequest struct {
	Agent      string
	Mutating   bool
	Authorized bool
	WorkDir    string
	Prompt     string
	Model      string
}

// Launch does not start a process. Mutating execution is refused because Codex
// and Claude own their files, subprocesses, and network. Read-only execution is
// also refused: the default Codex sandbox and Claude's permission rules are not
// AGT policy, and setting a working directory does not isolate the child.
func Launch(req LaunchRequest) error {
	name := req.Agent
	if name != "codex" && name != "claude" {
		name = "coding agent"
	}
	if req.Mutating {
		return fmt.Errorf("%w: %s owns its filesystem, subprocesses, and network; AGT has no tested mediation for those paths", ErrMutationUnavailable, name)
	}
	if !req.Authorized {
		return fmt.Errorf("%w: this process has no authorization to start %s", ErrLiveUnavailable, name)
	}
	return fmt.Errorf("%w: %s would run as a host child with its own sandbox and permission rules; AGT would not mediate each tool call", ErrLiveUnavailable, name)
}
