package redact

import "regexp"

var (
	pemRE    = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]*)PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]*)PRIVATE KEY-----`)
	akiaRE   = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	bearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	assignRE = regexp.MustCompile(`(?i)\b((?:api[_-]?key|secret|password|passwd|token|authorization))\b(\s*=\s*|\s*:\s+)(?:"[^"]*"|'[^']*'|\S+)`)
)

// Text hides high-confidence secrets before tool output is shown to a model
// or written into an event excerpt. It leaves ordinary source text in place.
func Text(s string) string {
	s = pemRE.ReplaceAllString(s, "[redacted private key]")
	s = akiaRE.ReplaceAllString(s, "[redacted]")
	s = bearerRE.ReplaceAllString(s, "Bearer [redacted]")
	s = assignRE.ReplaceAllString(s, "${1}${2}[redacted]")
	return s
}
