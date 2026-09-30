package usage

import "testing"

func TestLunaShapeExample(t *testing.T) {
	// 40k input at $0.10/M and 8k output at $0.50/M is $0.008.
	in := CostMicros(40_000, 100_000)
	out := CostMicros(8_000, 500_000)
	if in+out != 8_000 {
		t.Fatalf("cost micros = %d, want 8000 ($0.008)", in+out)
	}
	if USD(in+out) != "$0.008000" {
		t.Fatalf("usd = %s", USD(in+out))
	}
}

func TestZeroTokens(t *testing.T) {
	if CostMicros(0, 100_000) != 0 {
		t.Fatal("zero tokens should cost nothing")
	}
	if CentsToMicros(100) != 1_000_000 {
		t.Fatal("100 cents is one dollar")
	}
}
