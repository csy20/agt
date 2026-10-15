package version

import (
	"crypto/sha256"
	"encoding/hex"
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
)

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
