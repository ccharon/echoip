package http

import (
	"container/list"
	"fmt"
	"hash/fnv"
	"net"
	"sync"
)

// Cache keeps the most recently built responses, keyed by address. A capacity
// of zero disables it.
type Cache struct {
	mu        sync.RWMutex
	capacity  int
	entries   map[uint64]*list.Element
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
		entries:  make(map[uint64]*list.Element),
		values:   list.New(),
	}
}

// key identifies an address regardless of whether it is held in 4 or 16 bytes,
// so the same address is never cached twice.
func key(ip net.IP) uint64 {
	h := fnv.New64a()
	if v := ip.To16(); v != nil {
		_, _ = h.Write(v)
	} else {
		_, _ = h.Write(ip)
	}
	return h.Sum64()
}

func (c *Cache) Set(ip net.IP, resp Response) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.capacity == 0 {
		return
	}

	k := key(ip)
	if current, ok := c.entries[k]; ok {
		c.values.Remove(current)
		delete(c.entries, k)
	}

	c.evict(len(c.entries) - c.capacity + 1)
	c.entries[k] = c.values.PushBack(resp)
}

func (c *Cache) Get(ip net.IP) (Response, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	el, ok := c.entries[key(ip)]
	if !ok {
		return Response{}, false
	}
	return el.Value.(Response), true
}

// Clear removes every entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[uint64]*list.Element)
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
		delete(c.entries, key(el.Value.(Response).IP))
		c.values.Remove(el)
		el = next
		c.evictions++
	}
}
