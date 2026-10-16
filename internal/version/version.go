package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/tools"
)

const (
	// Name is the product token a daemon must present. A bare HTTP 200 is not enough.
	Name = "agt"
	// Product is the release version. The client rejects a daemon on a different value.
	Product = "0.1.0"
	// ProductHeader and VersionHeader are set on /healthz and are not a secret.
	ProductHeader = "X-Agt-Product"
	VersionHeader = "X-Agt-Version"
	// Contract is the API contract. A matching product version is not enough.
	Contract = "api.v2"
	// ContractHeader, BuildHeader, and CapabilitiesHeader are set on /healthz.
	ContractHeader     = "X-Agt-Contract"
	BuildHeader        = "X-Agt-Build"
	CapabilitiesHeader = "X-Agt-Capabilities"
)

// Build is the local build identity stamped with -X at link time.
// An empty value makes BuildID read the module's VCS metadata instead.
var Build string

// Capabilities are the session and model features a current daemon must advertise.
func Capabilities() []string {
	return []string{"sessions", "models"}
}

// CapabilityHeaderValue is the health-header form of Capabilities.
func CapabilityHeaderValue() string {
	return strings.Join(Capabilities(), ",")
}

// ParseCapabilities splits a health header. An empty header advertises nothing.
func ParseCapabilities(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// MissingCapabilities returns required capabilities that are absent.
func MissingCapabilities(have []string) []string {
	got := map[string]bool{}
	for _, name := range have {
		got[name] = true
	}
	var missing []string
	for _, name := range Capabilities() {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// MissingAdvertised reports a contract or capability a daemon did not present.
func MissingAdvertised(contract string, have []string) []string {
	var missing []string
	if contract != Contract {
		missing = append(missing, Contract)
	}
	return append(missing, MissingCapabilities(have)...)
}

// IncompatibleDaemon tells the operator to rebuild and restart.
// It does not wrap an error that would start or stop another process.
func IncompatibleDaemon(base, productVersion string, missing []string) error {
	if strings.TrimSpace(productVersion) == "" {
		productVersion = Product
	}
	return fmt.Errorf("agtd at %s version %s does not advertise %s. Next: rebuild agtd from this checkout with `make build` and restart that daemon yourself. This command does not stop another process.",
		base, productVersion, strings.Join(missing, ", "))
}

// BuildID distinguishes a local or dirty build from a release version.
// Product stays 0.1.0. This id is not the agent version stored on a run.
func BuildID() string {
	if strings.TrimSpace(Build) != "" {
		return Build
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "local+unknown"
	}
	var rev, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if rev == "" {
		sum := sha256.Sum256([]byte(info.Main.Path + "\n" + info.Main.Version + "\n" + info.GoVersion))
		return "local+" + hex.EncodeToString(sum[:6])
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		return "local+" + rev + "-dirty"
	}
	return "local+" + rev
}

// Compute stamps the contracts and sandbox images that change agent behaviour.
func Compute(images ...string) string {
	lines := []string{
		Product,
		policy.Version,
		tools.Version,
		model.PromptVersion,
		model.TranscriptContract,
		patch.Contract,
		profile.Contract,
	}
	lines = append(lines, images...)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return Product + "+" + hex.EncodeToString(sum[:6])
}
