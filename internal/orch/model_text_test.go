package orch

import "strings"

import "testing"

func TestModelTextRedactsBeforeTheModel(t *testing.T) {
	out := modelText("password=supersecretvalue\nAKIAIOSFODNN7EXAMPLE", 500)
	if strings.Contains(out, "supersecretvalue") || strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("model text %q", out)
	}
}
