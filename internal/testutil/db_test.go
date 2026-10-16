package testutil

import (
	"strings"
	"testing"

	"github.com/csy20/agt/internal/store"
)

func TestDisposableNameRejectsTheSharedDatabase(t *testing.T) {
	refused := []string{
		"agt", "AGT", "postgres", "template0", "template1", "agt_test", "agt_m007",
		"agt_m005_fresh", "agt_m005_upgrade", "agt_m006_fresh", "agt_m007_upgrade",
	}
	for _, name := range refused {
		if store.DisposableName(name) {
			t.Fatalf("accepted %q", name)
		}
		if _, err := store.QuoteDisposable(name); err == nil {
			t.Fatalf("quoted %q", name)
		}
		if err := store.AuthorizeReset("token-value", name, "token-value", store.HarnessPurpose); err == nil {
			t.Fatalf("authorized reset of %q", name)
		}
	}
	if !store.DisposableName("agt_test_m005_fresh") || !store.DisposableName("agt_test_abcdef") {
		t.Fatal("rejected a disposable name")
	}
	if err := store.AuthorizeReset("", "agt_test_abcdef", "", store.HarnessPurpose); err == nil {
		t.Fatal("empty token was authorized")
	}
	raw := "postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable"
	got, err := WithDatabase(raw, "postgres")
	if err != nil || !strings.HasSuffix(strings.Split(got, "?")[0], "/postgres") || strings.Contains(got, "/agt?") {
		t.Fatalf("server URL %s err %v", got, err)
	}
	if _, err := WithDatabase(raw, "agt"); err == nil {
		t.Fatal("WithDatabase accepted agt")
	}
}
