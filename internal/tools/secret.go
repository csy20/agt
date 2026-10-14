package tools

import (
	"path/filepath"
	"strings"
)

// SecretDir reports credential directories that stay out of the workspace.
func SecretDir(name string) bool {
	switch strings.ToLower(name) {
	case ".ssh", ".aws", ".azure", ".gnupg", ".kube", ".docker":
		return true
	default:
		return false
	}
}

// SecretFile reports filenames whose contents are not shown to the agent.
// .env.example, .env.sample, and .env.template stay visible.
func SecretFile(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".git-credentials",
		"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
		"credentials.json", "credentials.yaml", "credentials.yml",
		"secrets.json", "secrets.yaml", "secrets.yml":
		return true
	}
	if strings.HasPrefix(lower, ".env.") {
		switch {
		case strings.HasSuffix(lower, ".example"),
			strings.HasSuffix(lower, ".sample"),
			strings.HasSuffix(lower, ".template"):
			return false
		default:
			return true
		}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

// SecretRel reports whether any path segment is a secret file or directory.
func SecretRel(rel string) bool {
	slash := filepath.ToSlash(rel)
	for _, part := range strings.Split(slash, "/") {
		if part == "" || part == "." {
			continue
		}
		if SecretDir(part) || SecretFile(part) {
			return true
		}
	}
	return false
}
