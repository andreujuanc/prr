package ui

import (
	"container/list"
	"sync"
)

// lruCache is a thread-safe LRU cache with a fixed capacity.
// When full, the least recently accessed entry is evicted.
type lruCache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List // front = most recent, back = least recent
}

type lruEntry struct {
	key   string
	value string
}

// newLRUCache creates a new LRU cache with the given capacity.
func newLRUCache(capacity int) *lruCache {
	return &lruCache{
		capacity: capacity,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// get retrieves a value from the cache. Returns the value and true if found,
// or empty string and false if not. Accessed entries are moved to the front.
func (c *lruCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(elem)
	return elem.Value.(*lruEntry).value, true
}

// set adds or updates a value in the cache. If the cache is at capacity,
// the least recently used entry is evicted.
func (c *lruCache) set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*lruEntry).value = value
		return
	}

	// Evict oldest if at capacity
	if c.order.Len() >= c.capacity {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*lruEntry).key)
		}
	}

	// Insert new entry
	entry := &lruEntry{key: key, value: value}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
}
