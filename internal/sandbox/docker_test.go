package sandbox_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/sandbox"
	"github.com/csy20/agt/internal/testutil"
	"github.com/csy20/agt/internal/tools"
	"github.com/csy20/agt/internal/workspace"
)

func TestSandboxRunsTestsAndStaysClosed(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker absent")
	}
	image := os.Getenv("AGT_IMAGE_PYTHON")
	if image == "" {
		image = os.Getenv("AGT_SANDBOX_IMAGE")
	}
	if image == "" {
		t.Skip("AGT_SANDBOX_IMAGE unset")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Fatalf("sandbox image %s is not available: %v", image, err)
	}

	fixture := testutil.Fixture(t)
	original, err := os.ReadFile(filepath.Join(fixture, "payments", "processor.py"))
	if err != nil {
		t.Fatal(err)
	}
	runID := id.New("sbx_")
	work, _, err := workspace.Materialize(fixture, t.TempDir(), runID)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sandbox.Runner{Images: profile.Images{Python: image}, ExtraLabel: "agt-test=sandbox"}
	py, err := profile.Resolve(profile.Request{
		Name: profile.NamePython,
		Argv: []string{"python", "-m", "unittest", "tests.test_retry"},
	}, profile.Images{Python: image}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runner.Destroy(context.Background(), runID)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stdout, stderr, code, err := runner.RunProfile(ctx, work, runID, py)
	if err != nil {
		t.Fatalf("first test: %v\n%s\n%s", err, stdout, stderr)
	}
	if code != 1 {
		t.Fatalf("buggy tree exit %d\n%s\n%s", code, stdout, stderr)
	}

	path, oldText, newText := patchArgs(t)
	msg, err := tools.Patch(work, path, oldText, newText)
	if err != nil || msg != "patched" {
		t.Fatalf("patch %q %v", msg, err)
	}
	stdout, stderr, code, err = runner.RunProfile(ctx, work, runID, py)
	if err != nil {
		t.Fatalf("second test: %v\n%s\n%s", err, stdout, stderr)
	}
	if code != 0 {
		t.Fatalf("patched tree exit %d\n%s\n%s", code, stdout, stderr)
	}
	after, err := os.ReadFile(filepath.Join(fixture, "payments", "processor.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("source fixture was modified")
	}

	t.Setenv("AGT_HOST_SENTINEL", "host-secret")
	stdout, stderr, code, err = runner.Exec(ctx, work, runID, probeSpec(image, `
import os, socket
s = socket.socket()
s.settimeout(2)
try:
    s.connect(("1.1.1.1", 443))
    print("NETWORK_OPEN")
except Exception:
    print("NETWORK_CLOSED")
print("SENTINEL=[" + os.environ.get("AGT_HOST_SENTINEL", "") + "]")
`))
	if err != nil || code != 0 {
		t.Fatalf("probe exit %d err %v\n%s\n%s", code, err, stdout, stderr)
	}
	if !strings.Contains(stdout, "NETWORK_CLOSED") {
		t.Fatalf("network probe\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "SENTINEL=[]") {
		t.Fatalf("host env leaked\n%s", stdout)
	}

	sibling := filepath.Join(filepath.Dir(work), "escape-sibling")
	marker := filepath.Join(t.TempDir(), "host-marker")
	_, _, _, _ = runner.Exec(ctx, work, runID, probeSpec(image, `
open("/workspace/../escape-sibling", "w").write("nope")
open(`+quote(marker)+`, "w").write("nope")
`))
	if _, err := os.Stat(sibling); !os.IsNotExist(err) {
		t.Fatalf("write escaped to %s (%v)", sibling, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("write escaped to %s (%v)", marker, err)
	}

	stdout, stderr, code, err = runner.Exec(ctx, work, runID, probeSpec(image, `
import os
open("/workspace/agt-write-probe", "w").write("ok\n")
print("UID", os.getuid())
`))
	if err != nil || code != 0 {
		t.Fatalf("write probe exit %d err %v\n%s\n%s", code, err, stdout, stderr)
	}
	if strings.Contains(stdout, "UID -1") {
		t.Fatalf("container uid\n%s", stdout)
	}
	probe, err := os.ReadFile(filepath.Join(work, "agt-write-probe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(probe) != "ok\n" {
		t.Fatalf("probe %q", probe)
	}
	ids, err := exec.Command("docker", "ps", "-aq", "--filter", "label=agt-run="+runID).Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(ids))
	if id == "" {
		t.Fatal("container missing before user inspect")
	}
	userOut, err := exec.Command("docker", "inspect", "--format", "{{.Config.User}}", id).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(userOut), "-1") || strings.TrimSpace(string(userOut)) == "" {
		t.Fatalf("docker user %q", userOut)
	}

	if err := runner.Destroy(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=agt-run="+runID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("container leaked: %s", out)
	}
}

func patchArgs(t *testing.T) (string, string, string) {
	t.Helper()
	var script model.Script
	if err := json.Unmarshal(testutil.FixScript(t), &script); err != nil {
		t.Fatal(err)
	}
	for _, step := range script.Steps {
		if step.Tool != "apply_patch" {
			continue
		}
		var args struct {
			Path string `json:"path"`
			Old  string `json:"old"`
			New  string `json:"new"`
		}
		if err := json.Unmarshal(step.Args, &args); err != nil {
			t.Fatal(err)
		}
		return args.Path, args.Old, args.New
	}
	t.Fatal("script has no apply_patch step")
	return "", "", ""
}

func probeSpec(image, program string) sandbox.ExecSpec {
	return sandbox.ExecSpec{
		Image:   image,
		Argv:    []string{"python", "-c", program},
		Env:     profile.EnvFor(profile.NamePython),
		Workdir: "/workspace",
		Memory:  "256m",
		CPUs:    "1",
		PIDs:    64,
	}
}

func quote(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}
