package argusacceptg

import (
	"errors"
	"reflect"
	"testing"
)

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	var evicted []string
	c := NewLRU[string, int](2, func(k string, _ int) { evicted = append(evicted, k) })
	c.Put("a", 1)
	c.Put("b", 2)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing")
	}
	c.Put("c", 3)
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if !reflect.DeepEqual(evicted, []string{"b"}) {
		t.Fatalf("evicted = %v", evicted)
	}
	if got := c.Keys(); !reflect.DeepEqual(got, []string{"c", "a"}) {
		t.Fatalf("keys = %v", got)
	}
	if !c.Remove("a") || c.Remove("a") || c.Len() != 1 {
		t.Fatal("remove")
	}
}

func TestRingOverwritesOldest(t *testing.T) {
	r := NewRing[int](3)
	for i := 1; i <= 4; i++ {
		r.Push(i)
	}
	if got := r.Slice(); !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Fatalf("slice = %v", got)
	}
	for _, want := range []int{2, 3, 4} {
		if v, err := r.Pop(); err != nil || v != want {
			t.Fatalf("pop = %d, %v", v, err)
		}
	}
	if _, err := r.Pop(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v", err)
	}
}
