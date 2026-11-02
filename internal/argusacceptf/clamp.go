// Package argusacceptf holds throwaway code for the Argus acceptance run; it is never merged.
package argusacceptf

// Clamp returns v limited to the closed range [lo, hi]. When lo > hi the bounds are swapped.
func Clamp(v, lo, hi int) int {
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
