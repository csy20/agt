package version

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/tools"
)

const Product = "0.1.0"

// Compute stamps the contracts that change agent behaviour.
func Compute(image string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		Product,
		policy.Version,
		tools.Version,
		model.PromptVersion,
		image,
	}, "\n")))
	return Product + "+" + hex.EncodeToString(sum[:6])
}
