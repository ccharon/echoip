package server

import (
	"fmt"
	"net/netip"
	"testing"
)

func TestCacheCapacity(t *testing.T) {
	tests := []struct {
		addCount, capacity, size int
		evictions                uint64
	}{
		{1, 0, 0, 0},
		{1, 2, 1, 0},
		{2, 2, 2, 0},
		{3, 2, 2, 1},
		{10, 5, 5, 5},
	}
	for i, tt := range tests {
		c := NewCache(tt.capacity)
		var responses []Response
		for i := range tt.addCount {
			ip := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i))
			r := Response{IP: ip}
			responses = append(responses, r)
			c.Set(ip, r)
		}
		if got := len(c.entries); got != tt.size {
			t.Errorf("#%d: len(entries) = %d, want %d", i, got, tt.size)
		}
		if got := c.evictions; got != tt.evictions {
			t.Errorf("#%d: evictions = %d, want %d", i, got, tt.evictions)
		}
		if tt.capacity > 0 && tt.addCount > tt.capacity && tt.capacity == tt.size {
			lastAdded := responses[tt.addCount-1]
			if _, ok := c.Get(lastAdded.IP); !ok {
				t.Errorf("#%d: Get(%s) = (_, %t), want (_, %t)", i, lastAdded.IP.String(), ok, !ok)
			}
			firstAdded := responses[0]
			if _, ok := c.Get(firstAdded.IP); ok {
				t.Errorf("#%d: Get(%s) = (_, %t), want (_, %t)", i, firstAdded.IP.String(), ok, !ok)
			}
		}
	}
}

func TestCacheDuplicate(t *testing.T) {
	c := NewCache(10)
	ip := netip.MustParseAddr("192.0.2.1")
	response := Response{IP: ip}
	c.Set(ip, response)
	c.Set(ip, response)
	want := 1
	if got := len(c.entries); got != want {
		t.Errorf("want %d entries, got %d", want, got)
	}
	if got := c.values.Len(); got != want {
		t.Errorf("want %d values, got %d", want, got)
	}
}

func TestCacheResize(t *testing.T) {
	c := NewCache(10)
	for i := 1; i <= 20; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i))
		r := Response{IP: ip}
		c.Set(ip, r)
	}
	if got, want := len(c.entries), 10; got != want {
		t.Errorf("want %d entries, got %d", want, got)
	}
	if got, want := c.evictions, uint64(10); got != want {
		t.Errorf("want %d evictions, got %d", want, got)
	}
	if err := c.Resize(5); err != nil {
		t.Fatal(err)
	}
	if got, want := c.evictions, uint64(0); got != want {
		t.Errorf("want %d evictions, got %d", want, got)
	}
	r := Response{IP: netip.MustParseAddr("192.0.2.42")}
	c.Set(r.IP, r)
	if got, want := len(c.entries), 5; got != want {
		t.Errorf("want %d entries, got %d", want, got)
	}
}

func TestCacheClear(t *testing.T) {
	c := NewCache(10)
	ip := netip.MustParseAddr("127.0.0.1")
	c.Set(ip, Response{IP: ip})

	c.Clear()

	if _, ok := c.Get(ip); ok {
		t.Error("expected the entry to be gone")
	}
	if got := c.Stats().Size; got != 0 {
		t.Errorf("Expected size 0, got %d", got)
	}

	// The cache stays usable.
	c.Set(ip, Response{IP: ip})
	if _, ok := c.Get(ip); !ok {
		t.Error("expected the entry to be cached again")
	}
}

func TestCacheKeyUnmapsIPv4(t *testing.T) {
	c := NewCache(10)
	addr := netip.MustParseAddr("192.0.2.1")
	c.Set(addr, Response{IP: addr})

	// The mapped form is a different netip.Addr, so callers must unmap before
	// they reach the cache. ipFromRequest does that.
	mapped := netip.AddrFrom16(addr.As16())
	if _, ok := c.Get(mapped); ok {
		t.Error("expected the mapped form to miss")
	}
	if _, ok := c.Get(mapped.Unmap()); !ok {
		t.Error("expected the unmapped form to hit")
	}
}

func TestCacheOverwriteDoesNotEvict(t *testing.T) {
	c := NewCache(2)
	first := netip.MustParseAddr("192.0.2.1")
	second := netip.MustParseAddr("192.0.2.2")
	c.Set(first, Response{IP: first})
	c.Set(second, Response{IP: second})

	c.Set(second, Response{IP: second})

	if _, ok := c.Get(first); !ok {
		t.Error("expected the older entry to survive overwriting the newer one")
	}
	if got, want := c.evictions, uint64(0); got != want {
		t.Errorf("want %d evictions, got %d", want, got)
	}
}

func TestCacheResizeDropsEntries(t *testing.T) {
	c := NewCache(10)
	for i := 1; i <= 10; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i))
		c.Set(ip, Response{IP: ip})
	}

	if err := c.Resize(3); err != nil {
		t.Fatal(err)
	}

	if got, want := len(c.entries), 3; got != want {
		t.Errorf("want %d entries after shrinking, got %d", want, got)
	}
	if got, want := c.values.Len(), 3; got != want {
		t.Errorf("want %d values after shrinking, got %d", want, got)
	}
	newest := netip.MustParseAddr("192.0.2.10")
	if _, ok := c.Get(newest); !ok {
		t.Error("expected the newest entry to survive shrinking")
	}
}

func TestCacheDisabled(t *testing.T) {
	c := NewCache(0)
	ip := netip.MustParseAddr("192.0.2.1")

	c.Set(ip, Response{IP: ip})

	if _, ok := c.Get(ip); ok {
		t.Error("expected a disabled cache to keep nothing")
	}
}

// A read has to spare its entry from the next eviction, which is the whole
// difference between this cache and one that drops in insertion order.
func TestCacheEvictsLeastRecentlyRead(t *testing.T) {
	c := NewCache(3)

	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")
	d := netip.MustParseAddr("192.0.2.3")
	e := netip.MustParseAddr("192.0.2.4")

	for _, addr := range []netip.Addr{a, b, d} {
		c.Set(addr, Response{IP: addr})
	}

	// a is the oldest by insertion. Reading it makes b the oldest by use.
	if _, ok := c.Get(a); !ok {
		t.Fatal("expected a to be cached")
	}

	c.Set(e, Response{IP: e})

	if _, ok := c.Get(a); !ok {
		t.Error("a was read last and must have survived")
	}
	if _, ok := c.Get(b); ok {
		t.Error("b was read longest ago and must have been evicted")
	}
	if got, want := c.evictions, uint64(1); got != want {
		t.Errorf("evictions = %d, want %d", got, want)
	}
}

// A capacity of zero is the documented way to switch the cache off, so it is
// valid rather than refused.
func TestCacheZeroCapacityIsValid(t *testing.T) {
	c := NewCache(0)
	if got := c.Stats().Capacity; got != 0 {
		t.Errorf("Expected capacity 0, got %d", got)
	}

	c = NewCache(4)
	if err := c.Resize(0); err != nil {
		t.Errorf("Expected resizing to 0 to succeed: %v", err)
	}
	if got := c.Stats().Capacity; got != 0 {
		t.Errorf("Expected capacity 0 after the resize, got %d", got)
	}
	if err := c.Resize(-1); err == nil {
		t.Error("Expected a negative capacity to be refused")
	}
}
