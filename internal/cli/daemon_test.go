package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsDaemonNamesPreferExe(t *testing.T) {
	names := daemonBinaryNames("windows")
	if len(names) < 2 || names[0] != "agtd.exe" || names[1] != "agtd" {
		t.Fatalf("names %v", names)
	}
	if got := daemonBinaryNames("linux"); len(got) != 1 || got[0] != "agtd" {
		t.Fatalf("linux names %v", got)
	}
}

func TestSelectDaemonFindsExeBesideClient(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agtd.exe")
	if err := os.WriteFile(exe, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := selectDaemon(dir, daemonBinaryNames("windows"))
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Fatalf("selected %s", got)
	}
}

func TestSelectDaemonFindsBareName(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agtd")
	if err := os.WriteFile(bin, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := selectDaemon(dir, daemonBinaryNames("windows"))
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Fatalf("selected %s", got)
	}
}

func TestDetachDaemonSetsAttributes(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	detachDaemon(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("daemon process was not detached from the client")
	}
}
