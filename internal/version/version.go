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

const Product = "0.1.0"

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
