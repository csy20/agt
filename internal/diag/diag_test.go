package diag

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactDatabaseURLHidesPassword(t *testing.T) {
	raw := "postgres://agt:super-secret@127.0.0.1:54329/agt?sslmode=disable"
	got := RedactDatabaseURL(raw)
	if strings.Contains(got, "super-secret") {
		t.Fatalf("password leaked: %s", got)
	}
	if !strings.Contains(got, "127.0.0.1:54329") || !strings.Contains(got, "redacted") {
		t.Fatalf("redacted url %s", got)
	}
	err := PostgresUnreachable(raw, errors.New("dial postgres://agt:super-secret@127.0.0.1:54329/agt: refused"))
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("diagnostic leaked password: %s", err)
	}
	if !strings.Contains(err.Error(), "docker compose") || !strings.Contains(err.Error(), "AGT_DATABASE_URL") {
		t.Fatalf("diagnostic %s", err)
	}
}

func TestModelConfigWarning(t *testing.T) {
	if ModelConfigWarning("fake", "", "") != "" {
		t.Fatal("fake provider warned")
	}
	if ModelConfigWarning("openai", "key", "model") != "" {
		t.Fatal("complete openai config warned")
	}
	msg := ModelConfigWarning("openai", "", "")
	if !strings.Contains(msg, "AGT_MODEL_API_KEY") || !strings.Contains(msg, "AGT_MODEL_NAME") {
		t.Fatalf("warning %s", msg)
	}
}
