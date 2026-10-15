package gocounter

import "testing"

func TestInc(t *testing.T) {
	if got := Inc(1); got != 2 {
		t.Fatalf("Inc(1) = %d, want 2", got)
	}
}
