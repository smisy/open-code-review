package argusacceptg

import "errors"

// ErrEmpty is returned by Pop on an empty ring.
var ErrEmpty = errors.New("argusacceptg: ring is empty")

// Ring is a fixed-size FIFO buffer that overwrites its oldest element when full.
// It is not safe for concurrent use.
type Ring[T any] struct {
	buf   []T
	head  int
	count int
}

// NewRing returns a ring that holds at most size elements. A size below 1 is treated as 1.
func NewRing[T any](size int) *Ring[T] {
	if size < 1 {
		size = 1
	}
	return &Ring[T]{buf: make([]T, size)}
}

// Push appends v and reports whether an older element was overwritten.
func (r *Ring[T]) Push(v T) bool {
	tail := (r.head + r.count) % len(r.buf)
	r.buf[tail] = v
	if r.count == len(r.buf) {
		r.head = (r.head + 1) % len(r.buf)
		return true
	}
	r.count++
	return false
}

// Pop removes and returns the oldest element.
func (r *Ring[T]) Pop() (T, error) {
	var zero T
	if r.count == 0 {
		return zero, ErrEmpty
	}
	v := r.buf[r.head]
	r.buf[r.head] = zero
	r.head = (r.head + 1) % len(r.buf)
	r.count--
	return v, nil
}

// Len returns the number of elements held.
func (r *Ring[T]) Len() int { return r.count }

// Slice returns the elements from oldest to newest.
func (r *Ring[T]) Slice() []T {
	out := make([]T, r.count)
	for i := range out {
		out[i] = r.buf[(r.head+i)%len(r.buf)]
	}
	return out
}
