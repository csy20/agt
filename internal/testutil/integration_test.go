package testutil

import "testing"

func TestMissingPrereqFailsClosed(t *testing.T) {
	if !missingPrereq(true, false) || !missingPrereq(false, true) || missingPrereq(false, false) {
		t.Fatal("missingPrereq truth table")
	}
	t.Setenv("AGT_INTEGRATION", "")
	if MissingPrereq(false) {
		t.Fatal("an ordinary unit run may skip a dependency it did not configure")
	}
	if !MissingPrereq(true) {
		t.Fatal("an explicitly configured dependency must fail when it is missing")
	}
	t.Setenv("AGT_INTEGRATION", "1")
	if !MissingPrereq(false) {
		t.Fatal("AGT_INTEGRATION=1 must fail when a dependency is missing")
	}
}
