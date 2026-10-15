package diag

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/csy20/agt/internal/model"
)

// PostgresUnreachable describes a failed database connection without the password.
func PostgresUnreachable(databaseURL string, cause error) error {
	return fmt.Errorf("cannot reach Postgres at %s. Start it with `docker compose -f deploy/compose.yaml up -d --wait` or set AGT_DATABASE_URL. %s",
		RedactDatabaseURL(databaseURL), redactCause(databaseURL, cause))
}

// ModelConfigWarning is empty when the selected provider has the settings it needs.
// A loopback AGT_MODEL_BASE_URL may omit the API key. A cloud endpoint may not.
func ModelConfigWarning(provider, apiKey, modelName, baseURL string) string {
	if provider != "openai" {
		return ""
	}
	if modelName != "" && (apiKey != "" || model.LoopbackURL(baseURL)) {
		return ""
	}
	return "AGT_PROVIDER=openai needs AGT_MODEL_NAME. A cloud AGT_MODEL_BASE_URL also needs AGT_MODEL_API_KEY. A loopback base URL may omit the key. Restart agtd after changing them. The default base URL is https://api.openai.com/v1"
}

// RedactDatabaseURL hides a password embedded in a Postgres URL.
func RedactDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "AGT_DATABASE_URL"
	}
	if u.User != nil {
		name := u.User.Username()
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(name, "redacted")
		}
	}
	return u.String()
}

func redactCause(databaseURL string, cause error) string {
	if cause == nil {
		return "connection failed"
	}
	msg := cause.Error()
	u, err := url.Parse(databaseURL)
	if err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "redacted")
		}
	}
	return msg
}
