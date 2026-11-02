package argusacceptf

import "testing"

func TestClamp(t *testing.T) {
	cases := []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{5, 10, 0, 5},
		{0, 0, 0, 0},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

func TestClampPositive(t *testing.T) {
	if got := ClampPositive(-3, 10); got != 0 {
		t.Errorf("ClampPositive(-3, 10) = %d, want 0", got)
	}
}
