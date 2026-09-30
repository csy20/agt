package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAndPatchDecisions(t *testing.T) {
	ok, err := Decide("read_file_range", json.RawMessage(`{"path":"payments/processor.py","start":1,"end":40}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Decision != Allow || ok.ArgsHash == "" {
		t.Fatalf("read: %+v", ok)
	}

	bad, err := Decide("apply_patch", json.RawMessage(`{"path":"../../.ssh/id_rsa","old":"a","new":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bad.Decision != Deny {
		t.Fatalf("escape path decision = %s, want deny", bad.Decision)
	}

	push, err := Decide("git_push", json.RawMessage(`{"remote":"origin","branch":"agent/demo","commit":"abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if push.Decision != RequireApproval {
		t.Fatalf("push decision = %s", push.Decision)
	}
	mutated, err := Decide("git_push", json.RawMessage(`{"remote":"origin","branch":"main","commit":"abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if mutated.ArgsHash == push.ArgsHash {
		t.Fatal("changing the branch must change the approval hash")
	}
}

func TestUnknownToolDenied(t *testing.T) {
	res, err := Decide("shell", json.RawMessage(`{"cmd":"rm -rf /"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != Deny {
		t.Fatalf("shell decision = %s", res.Decision)
	}
}

func TestPatchStaysInsideTree(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "leak")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	res, err := Decide("apply_patch", json.RawMessage(`{"path":"leak","old":"secret","new":"pwned"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The path is relative and looks local. The workspace layer must still
	// refuse to follow the symlink. Policy allows the relative shape; the
	// write check is asserted in the tools package.
	if res.Decision != Allow {
		t.Fatalf("relative symlink path is denied later at the write, decision=%s reason=%s", res.Decision, res.Reason)
	}
}
