package server

import (
	"container/list"
	"fmt"
	"net/netip"
	"sync"
)

// Cache keeps responses by address, which saves the reverse lookup. A capacity
// of zero disables it. A full cache drops the entry read longest ago, so a
// client that keeps asking survives any number of one-time visitors.
type Cache struct {
	mu        sync.RWMutex
	capacity  int
	entries   map[netip.Addr]*list.Element
	values    *list.List
	evictions uint64
}

// cacheEntry carries its own key, so eviction does not have to read it back
// out of the response.
type cacheEntry struct {
	addr     netip.Addr
	response Response
}

// cacheStats is a snapshot of the cache for the debug handler.
type cacheStats struct {
	Size      int    `json:"size"`
	Capacity  int    `json:"capacity"`
	Evictions uint64 `json:"evictions"`
}

// NewCache returns an empty cache. A capacity below zero counts as zero.
func NewCache(capacity int) *Cache {
	if capacity < 0 {
		capacity = 0
	}
	return &Cache{
		capacity: capacity,
		entries:  make(map[netip.Addr]*list.Element),
		values:   list.New(),
	}
}

// set stores resp for addr, evicting the entry read longest ago when full.
func (c *Cache) set(addr netip.Addr, resp Response) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.capacity == 0 {
		return
	}

	if current, ok := c.entries[addr]; ok {
		c.values.Remove(current)
		delete(c.entries, addr)
	}

	c.evict(len(c.entries) - c.capacity + 1)
	c.entries[addr] = c.values.PushBack(&cacheEntry{addr: addr, response: resp})
}

// get takes the write lock, because a hit moves its entry to the back of the
// eviction order.
func (c *Cache) get(addr netip.Addr) (Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[addr]
	if !ok {
		return Response{}, false
	}
	c.values.MoveToBack(el)

	return el.Value.(*cacheEntry).response, true
}

// Clear removes every entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[netip.Addr]*list.Element)
	c.values.Init()
}

// resize changes the capacity, dropping the oldest entries that no longer fit.
// The eviction counter starts over, so it measures the new capacity.
func (c *Cache) resize(capacity int) error {
	if capacity < 0 {
		return fmt.Errorf("invalid capacity: %d", capacity)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.capacity = capacity
	c.evict(len(c.entries) - capacity)
	c.evictions = 0

	return nil
}

// stats returns the current size, capacity and evictions since the last
// resize.
func (c *Cache) stats() cacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return cacheStats{
		Size:      len(c.entries),
		Capacity:  c.capacity,
		Evictions: c.evictions,
	}
}

// evict drops the n entries that were read longest ago. The caller holds the
// write lock.
func (c *Cache) evict(n int) {
	for el := c.values.Front(); n > 0 && el != nil; n-- {
		next := el.Next()
		delete(c.entries, el.Value.(*cacheEntry).addr)
		c.values.Remove(el)
		el = next
		c.evictions++
	}
}
