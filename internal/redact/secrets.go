package redact

import "strings"

// Hide removes known credential values after the pattern redactor runs.
// Values shorter than 8 characters are left in place so ordinary words survive.
func Hide(s string, secrets []string) string {
	s = Text(s)
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}
