package version

import (
	"strings"
	"testing"
)

func TestMissingCapabilitiesKeepsTheRequiredNames(t *testing.T) {
	got := MissingCapabilities([]string{"sessions"})
	if len(got) != 1 || got[0] != "models" {
		t.Fatalf("missing %#v", got)
	}
	if len(MissingCapabilities(Capabilities())) != 0 {
		t.Fatal("a current daemon was reported incomplete")
	}
	advertised := MissingAdvertised("", nil)
	if len(advertised) != 3 || advertised[0] != Contract || advertised[1] != "sessions" || advertised[2] != "models" {
		t.Fatalf("advertised %#v", advertised)
	}
}

func TestBuildIDUsesTheLinkStamp(t *testing.T) {
	old := Build
	Build = "local+abc123def456-dirty.20261016T000000Z"
	t.Cleanup(func() { Build = old })
	if BuildID() != Build {
		t.Fatalf("build id %s", BuildID())
	}
}

func TestBuildIDReadsVCSWhenTheStampIsEmpty(t *testing.T) {
	old := Build
	Build = ""
	t.Cleanup(func() { Build = old })
	got := BuildID()
	if len(got) < len("local+") || got[:6] != "local+" {
		t.Fatalf("build id %s", got)
	}
}

func TestIncompatibleDaemonDoesNotAskToStopAProcess(t *testing.T) {
	err := IncompatibleDaemon("http://127.0.0.1:9", Product, []string{"sessions", "models"})
	text := err.Error()
	for _, part := range []string{"http://127.0.0.1:9", Product, "does not advertise", "sessions", "models", "Next: rebuild agtd from this checkout", "This command does not stop another process."} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q in %s", part, text)
		}
	}
}
