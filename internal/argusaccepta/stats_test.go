package argusaccepta

import (
	"errors"
	"math"
	"testing"
)

func TestStats(t *testing.T) {
	xs := []float64{4, 1, 3, 2}
	if m, _ := Mean(xs); m != 2.5 {
		t.Errorf("mean = %v", m)
	}
	if m, _ := Median(xs); m != 2.5 {
		t.Errorf("median = %v", m)
	}
	if sd, _ := StdDev(xs); math.Abs(sd-math.Sqrt(1.25)) > 1e-12 {
		t.Errorf("stddev = %v", sd)
	}
	if p, _ := Percentile(xs, 50); p != 2.5 {
		t.Errorf("p50 = %v", p)
	}
	if p, _ := Percentile(xs, 100); p != 4 {
		t.Errorf("p100 = %v", p)
	}
	if _, err := Mean(nil); !errors.Is(err, ErrNoData) {
		t.Errorf("err = %v", err)
	}
	if xs[0] != 4 {
		t.Error("input modified")
	}
}
