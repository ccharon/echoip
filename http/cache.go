package http

import (
	"container/list"
	"fmt"
	"net/netip"
	"sync"
)

// Cache keeps the most recently built responses, keyed by address. A capacity
// of zero disables it.
type Cache struct {
	mu        sync.RWMutex
	capacity  int
	entries   map[netip.Addr]*list.Element
	values    *list.List
	evictions uint64
}

type CacheStats struct {
	Capacity  int
	Size      int
	Evictions uint64
}

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

func (c *Cache) Set(addr netip.Addr, resp Response) {
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
	c.entries[addr] = c.values.PushBack(resp)
}

func (c *Cache) Get(addr netip.Addr) (Response, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	el, ok := c.entries[addr]
	if !ok {
		return Response{}, false
	}
	return el.Value.(Response), true
}

// Clear removes every entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[netip.Addr]*list.Element)
	c.values.Init()
}

// Resize changes the capacity, dropping the oldest entries that no longer fit.
func (c *Cache) Resize(capacity int) error {
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

func (c *Cache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return CacheStats{
		Size:      len(c.entries),
		Capacity:  c.capacity,
		Evictions: c.evictions,
	}
}

// evict drops the n oldest entries. The caller holds the write lock.
func (c *Cache) evict(n int) {
	for el := c.values.Front(); n > 0 && el != nil; n-- {
		next := el.Next()
		delete(c.entries, el.Value.(Response).IP)
		c.values.Remove(el)
		el = next
		c.evictions++
	}
}
