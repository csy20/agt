package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CodingAgent is a local CLI probe. Authentication is not-probed: this code
// does not read Codex or Claude credential files.
type CodingAgent struct {
	Name             string
	Protocol         string
	Installed        bool
	Version          string
	VersionSupported bool
	Authentication   string
	Reason           string
}

// VersionFunc returns a runtime version. found is false when the executable is absent.
// The production function runs `<name> --version` and passes no prompt.
type VersionFunc func(name string) (version string, found bool, err error)

// Probe reports codex and claude. It does not start a task.
func Probe(versionOf VersionFunc) []CodingAgent {
	if versionOf == nil {
		versionOf = DefaultVersion
	}
	return []CodingAgent{
		describe("codex", ProtocolCodex, versionOf),
		describe("claude", ProtocolClaude, versionOf),
	}
}

func describe(name, protocol string, versionOf VersionFunc) CodingAgent {
	item := CodingAgent{
		Name:           name,
		Protocol:       protocol,
		Authentication: "not-probed",
	}
	version, found, err := versionOf(name)
	item.Installed = found
	item.Version = version
	switch {
	case !found:
		item.Reason = name + " executable was not found. Live execution stays unavailable."
	case err != nil:
		item.Reason = name + " --version failed. Live execution stays unavailable."
	default:
		item.VersionSupported = supportedRuntime(name, version)
		if item.VersionSupported && name == "codex" {
			item.Reason = "codex fixtures are scripted. agt agent ask starts app-server only after --authorize-live. It uses permission profile agt-snapshot, disables shell_tool, and sends one prompt-only turn with tagged excerpts after command/exec proves the snapshot read works and sibling, symlink, traversal, home, write, and network probes fail. A stub or an unvalidated runtime is labelled scripted. Mutation stays unavailable. Listing agents does not start Codex."
		} else if item.VersionSupported {
			item.Reason = name + " can be parsed from scripted " + protocol + " fixtures. Live and mutating execution stay unavailable: the CLI owns its subprocesses, filesystem, and network, and AGT does not mediate them. Claude hooks, deny rules, ask rules, permission mode, and allow rules run before a canUseTool callback; this adapter does not install that callback."
		} else {
			item.Reason = name + " runtime " + empty(version) + " is outside the pinned documentation target. Live execution stays unavailable."
		}
	}
	return item
}

func supportedRuntime(name, version string) bool {
	switch name {
	case "codex":
		return strings.HasPrefix(semverPattern.FindString(version), "0.162.")
	case "claude":
		return claudeVersionSupported(version)
	default:
		return false
	}
}

var semverPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

func parseSemver(v string) (major, minor, patch int, ok bool) {
	match := semverPattern.FindString(v)
	if match == "" {
		return 0, 0, 0, false
	}
	_, err := fmt.Sscanf(match, "%d.%d.%d", &major, &minor, &patch)
	return major, minor, patch, err == nil
}

// DefaultVersion runs `codex --version` or `claude --version` with a two-second
// limit. The argument list is only --version. Credential variables are not copied
// into the child. A missing executable is found=false and a nil error.
func DefaultVersion(name string) (string, bool, error) {
	if name != "codex" && name != "claude" {
		return "", false, fmt.Errorf("refusing to probe %q", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = probeEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", true, err
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:80]
	}
	return line, true, nil
}

func probeEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER", "LOGNAME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
