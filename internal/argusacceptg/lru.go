// Package argusacceptg holds throwaway code for the Argus acceptance run; it is never merged.
package argusacceptg

import (
	"container/list"
	"sync"
)

// LRU is a fixed-size, concurrency-safe least-recently-used cache.
type LRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[K]*list.Element
	onEvict  func(K, V)
}

type entry[K comparable, V any] struct {
	key   K
	value V
}

// NewLRU returns a cache holding at most capacity entries. A capacity below 1 is treated as 1.
// onEvict, when not nil, is called for each entry the cache drops to make room.
func NewLRU[K comparable, V any](capacity int, onEvict func(K, V)) *LRU[K, V] {
	if capacity < 1 {
		capacity = 1
	}
	return &LRU[K, V]{
		capacity: capacity,
		order:    list.New(),
		items:    make(map[K]*list.Element, capacity),
		onEvict:  onEvict,
	}
}

// Get returns the value for key and marks it as most recently used.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*entry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Put stores value under key, evicting the least recently used entry when the cache is full.
func (c *LRU[K, V]) Put(key K, value V) {
	var evicted *entry[K, V]
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		el.Value.(*entry[K, V]).value = value
		c.order.MoveToFront(el)
		c.mu.Unlock()
		return
	}
	c.items[key] = c.order.PushFront(&entry[K, V]{key: key, value: value})
	if c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		evicted = oldest.Value.(*entry[K, V])
		delete(c.items, evicted.key)
	}
	onEvict := c.onEvict
	c.mu.Unlock()
	if evicted != nil && onEvict != nil {
		onEvict(evicted.key, evicted.value)
	}
}

// Remove deletes key and reports whether it was present. It does not call onEvict.
func (c *LRU[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return false
	}
	c.order.Remove(el)
	delete(c.items, key)
	return true
}

// Len returns the number of entries.
func (c *LRU[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Keys returns the keys from most to least recently used.
func (c *LRU[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]K, 0, c.order.Len())
	for el := c.order.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*entry[K, V]).key)
	}
	return keys
}
