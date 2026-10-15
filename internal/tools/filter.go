package tools

// excludedDirs are dependency, VCS, and build directories. Project
// configuration such as .github and .gitignore is not in this set.
// The names match anywhere in the tree, not only at the repository root.
var excludedDirs = map[string]struct{}{
	".git":          {},
	".gradle":       {},
	".mypy_cache":   {},
	".next":         {},
	".pytest_cache": {},
	".ruff_cache":   {},
	".tox":          {},
	".turbo":        {},
	".venv":         {},
	"__pycache__":   {},
	"build":         {},
	"coverage":      {},
	"dist":          {},
	"node_modules":  {},
	"target":        {},
	"vendor":        {},
	"venv":          {},
}

// SkipDir reports whether a directory name is left out of copy, search, and diff.
func SkipDir(name string) bool {
	_, ok := excludedDirs[name]
	return ok
}
