package tools

import (
	"path"
	"strings"
)

// IsTestPath reports a file the user should review before the agent changes it.
// A passing run that rewrote its own test is not, by itself, a verified repair.
func IsTestPath(rel string) bool {
	slash := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if slash == "." || strings.HasPrefix(slash, "../") || slash == ".." {
		return false
	}
	base := strings.ToLower(path.Base(slash))
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".test.mjs"),
		strings.HasSuffix(base, ".test.cjs"), strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".spec.js"), strings.HasSuffix(base, ".spec.ts"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	}
	if slash == "tests" || strings.HasPrefix(slash, "tests/") || strings.Contains(slash, "/tests/") {
		return true
	}
	return false
}
