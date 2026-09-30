package cli

import "testing"

func TestFormatApprovalRequest(t *testing.T) {
	got := formatEvent("approval.requested", []byte(`{"tool":"git_push","command_id":"cmd_1","args_hash":"abc"}`))
	want := "approval required git_push cmd_1 hash abc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
