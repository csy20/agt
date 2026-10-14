package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPreflightReportsMissingImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := preflight(ctx, "agt-sandbox:does-not-exist")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		if !strings.Contains(msg, "docker") || !strings.Contains(msg, "does not fall back") {
			t.Fatal(msg)
		}
		return
	}
	if !strings.Contains(msg, "agt-sandbox:does-not-exist") || !strings.Contains(msg, "make image") {
		t.Fatal(msg)
	}
	if strings.Contains(msg, "host python") {
		t.Fatal(msg)
	}
}
