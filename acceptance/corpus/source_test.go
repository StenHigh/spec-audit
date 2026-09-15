package fixture

import "testing"

func TestFlip(t *testing.T) {
	for _, tc := range []struct {
		input bool
		want  bool
	}{
		{false, true},
		{true, false},
	} {
		if got := Flip(tc.input); got != tc.want {
			t.Fatalf("Flip(%v) = %v; want %v", tc.input, got, tc.want)
		}
	}
}

func TestDouble(t *testing.T) {
	if got := Double(7); got <= 0 {
		t.Fatalf("Double(7) = %d; want a positive value", got)
	}
}

func TestAbsolute(t *testing.T) {
	if got := Absolute(-3); got != -3 {
		t.Fatalf("Absolute(-3) = %d; want -3", got)
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize(" Ada "); got != "Ada" {
		t.Fatalf("Normalize returned %q; want %q", got, "Ada")
	}
}
