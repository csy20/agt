package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if push.Decision != Deny || !strings.Contains(push.Reason, "unsupported") {
		t.Fatalf("push decision = %s reason %s", push.Decision, push.Reason)
	}
	mutated, err := Decide("git_push", json.RawMessage(`{"remote":"origin","branch":"main","commit":"abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if mutated.ArgsHash == push.ArgsHash {
		t.Fatal("changing the branch must change the approval hash")
	}
}

func TestSecretPathsAreDenied(t *testing.T) {
	denied, err := Decide("read_file_range", json.RawMessage(`{"path":".env","start":1,"end":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if denied.Decision != Deny || !strings.Contains(denied.Reason, "secret") {
		t.Fatalf("env decision %+v", denied)
	}
	nested, err := Decide("apply_patch", json.RawMessage(`{"path":".ssh/id_rsa","old":"a","new":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if nested.Decision != Deny {
		t.Fatalf("key decision %+v", nested)
	}
	ok, err := Decide("read_file_range", json.RawMessage(`{"path":".env.example","start":1,"end":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Decision != Allow {
		t.Fatalf("example decision %+v", ok)
	}
}

func TestWriteAndDeleteStayInsideTheTree(t *testing.T) {
	ok, err := Decide("write_file", json.RawMessage(`{"path":"counter.go","content":"package p\n","before_sha256":""}`))
	if err != nil || ok.Decision != Allow {
		t.Fatalf("write %+v %v", ok, err)
	}
	secret, err := Decide("delete_file", json.RawMessage(`{"path":".env","before_sha256":"`+strings.Repeat("ab", 32)+`"}`))
	if err != nil || secret.Decision != Deny {
		t.Fatalf("secret delete %+v %v", secret, err)
	}
	shell, err := Decide("write_file", json.RawMessage(`{"path":"../x","content":"a","before_sha256":""}`))
	if err != nil || shell.Decision != Deny {
		t.Fatalf("escape write %+v %v", shell, err)
	}
}

func TestTestEditsRequireApproval(t *testing.T) {
	write, err := Decide("write_file", json.RawMessage(`{"path":"tests/test_retry.py","content":"x","before_sha256":""}`))
	if err != nil || write.Decision != RequireApproval {
		t.Fatalf("python test write %+v %v", write, err)
	}
	patch, err := Decide("apply_patch", json.RawMessage(`{"path":"counter_test.go","old":"a","new":"b"}`))
	if err != nil || patch.Decision != RequireApproval {
		t.Fatalf("go test patch %+v %v", patch, err)
	}
	del, err := Decide("delete_file", json.RawMessage(`{"path":"quote.test.js","before_sha256":"`+strings.Repeat("ab", 32)+`"}`))
	if err != nil || del.Decision != RequireApproval {
		t.Fatalf("js test delete %+v %v", del, err)
	}
	if write.ArgsHash == "" || write.ArgsHash != hash("write_file", write.Canonical) {
		t.Fatal("approval hash is not bound to the canonical arguments")
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
