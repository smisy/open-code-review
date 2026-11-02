package argusaccepta

import (
	"errors"
	"math"
	"sort"
)

// ErrNoData is returned when a statistic is asked of an empty sample.
var ErrNoData = errors.New("argusaccepta: no data")

// Mean returns the arithmetic mean of xs.
func Mean(xs []float64) (float64, error) {
	if len(xs) == 0 {
		return 0, ErrNoData
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs)), nil
}

// Median returns the median of xs without modifying it.
func Median(xs []float64) (float64, error) {
	if len(xs) == 0 {
		return 0, ErrNoData
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid], nil
	}
	return (s[mid-1] + s[mid]) / 2, nil
}

// StdDev returns the population standard deviation of xs.
func StdDev(xs []float64) (float64, error) {
	m, err := Mean(xs)
	if err != nil {
		return 0, err
	}
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs))), nil
}

// Percentile returns the p-th percentile (0 <= p <= 100) of xs by linear interpolation
// between closest ranks. It does not modify xs.
func Percentile(xs []float64, p float64) (float64, error) {
	if len(xs) == 0 {
		return 0, ErrNoData
	}
	if p < 0 || p > 100 || math.IsNaN(p) {
		return 0, errors.New("argusaccepta: percentile out of range")
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	rank := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return s[lo], nil
	}
	frac := rank - float64(lo)
	return s[lo] + frac*(s[hi]-s[lo]), nil
}
