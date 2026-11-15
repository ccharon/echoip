package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinates(t *testing.T) {
	tests := []struct {
		name string
		lat  *float64
		lon  *float64
		want string
	}{
		{"both", new(63.416667), new(10.416667), "63.416667,10.416667"},
		{"southern and western", new(-29.0), new(-82.3925), "-29.000000,-82.392500"},
		{"zero is a place", new(0.0), new(0.0), "0.000000,0.000000"},
		{"latitude alone", new(63.416667), nil, ""},
		{"longitude alone", nil, new(10.416667), ""},
		{"neither", nil, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Response{Latitude: tt.lat, Longitude: tt.lon}
			if got := r.Coordinates(); got != tt.want {
				t.Errorf("Coordinates() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A client that leaves cancels the reverse lookup, and the answer it cut short
// must not be cached without its hostname.
func TestCancelledLookupIsNotCached(t *testing.T) {
	cfg := Config{LookupAddr: func(ctx context.Context, _ netip.Addr) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "localhost", nil
	}}
	srv := New(cfg, &testDB{}, NewCache(10))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/json", nil)

	response, err := srv.newResponse(req, true)
	if err != nil {
		t.Fatal(err)
	}
	if response.Hostname != "" {
		t.Errorf("expected no hostname after the client left, got %q", response.Hostname)
	}
	if size := srv.cache.stats().Size; size != 0 {
		t.Errorf("expected nothing cached, got %d entries", size)
	}

	response, err = srv.newResponse(httptest.NewRequest(http.MethodGet, "/json", nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if response.Hostname != "localhost" {
		t.Errorf("expected the hostname on the next request, got %q", response.Hostname)
	}
}

// A field endpoint skips the reverse lookup and leaves its response uncached.
func TestFieldEndpointSkipsReverseLookup(t *testing.T) {
	lookups := 0
	cfg := Config{City: true, LookupAddr: func(context.Context, netip.Addr) (string, error) {
		lookups++
		return "localhost", nil
	}}
	srv := New(cfg, &testDB{}, NewCache(10))
	h := srv.Handler()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/country", nil))
	if lookups != 0 {
		t.Errorf("expected no reverse lookup for /country, got %d", lookups)
	}
	if size := srv.cache.stats().Size; size != 0 {
		t.Errorf("expected nothing cached after /country, got %d entries", size)
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/json", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/country", nil))
	if lookups != 1 {
		t.Errorf("expected one reverse lookup for /json, got %d", lookups)
	}
}

// Without a resolver a field endpoint's response is complete and is cached.
func TestFieldEndpointCachesWithoutResolver(t *testing.T) {
	srv := New(Config{City: true}, &testDB{}, NewCache(10))
	srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/country", nil))
	if size := srv.cache.stats().Size; size != 1 {
		t.Errorf("expected the response cached, got %d entries", size)
	}
}

// blockingLookup stands in for a resolver that does not answer until release
// is closed. It counts the lookups in flight and the most seen at once.
type blockingLookup struct {
	started  chan netip.Addr
	release  chan struct{}
	inFlight atomic.Int32
	most     atomic.Int32
}

func newBlockingLookup() *blockingLookup {
	return &blockingLookup{started: make(chan netip.Addr, 100), release: make(chan struct{})}
}

func (b *blockingLookup) lookupAddr(ctx context.Context, addr netip.Addr) (string, error) {
	n := b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	for {
		most := b.most.Load()
		if n <= most || b.most.CompareAndSwap(most, n) {
			break
		}
	}
	b.started <- addr
	select {
	case <-b.release:
		return "host-" + addr.String(), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// getJSON asks for /json about ip and fails the test if the answer takes
// longer than a blocked lookup would allow.
func getJSON(t *testing.T, h http.Handler, ip string) (int, Response) {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/json?ip="+ip, nil))
		done <- w
	}()

	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("request for %s waited on a reverse lookup", ip)
	}

	var response Response
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding the answer for %s: %v", ip, err)
	}
	return w.Code, response
}

// Over the cap a request answers without the hostname instead of waiting,
// and that answer is not cached, so the hostname appears once lookups are
// free again.
func TestReverseLookupsAreCapped(t *testing.T) {
	const limit = 3
	lookup := newBlockingLookup()
	srv := New(Config{LookupAddr: lookup.lookupAddr}, &testDB{}, NewCache(100))
	srv.lookups = make(chan struct{}, limit)
	h := srv.Handler()

	busy := []string{"1.1.1.1", "1.0.0.1", "8.8.8.8"}
	var wg sync.WaitGroup
	for _, ip := range busy {
		wg.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/json?ip="+ip, nil))
		})
	}
	for range busy {
		<-lookup.started
	}

	const saturated = "9.9.9.9"
	status, response := getJSON(t, h, saturated)
	if status != http.StatusOK {
		t.Errorf("expected 200 over the cap, got %d", status)
	}
	if response.Hostname != "" {
		t.Errorf("expected no hostname over the cap, got %q", response.Hostname)
	}
	if response.City == "" {
		t.Errorf("expected the GeoIP data over the cap, got %+v", response)
	}
	if _, cached := srv.cache.get(netip.MustParseAddr(saturated)); cached {
		t.Error("expected the answer without hostname to stay out of the cache")
	}

	close(lookup.release)
	wg.Wait()

	if most := lookup.most.Load(); most > limit {
		t.Errorf("expected at most %d lookups at once, got %d", limit, most)
	}
	if size := srv.cache.stats().Size; size != len(busy) {
		t.Errorf("expected the %d finished lookups cached, got %d entries", len(busy), size)
	}

	_, response = getJSON(t, h, saturated)
	if want := "host-" + saturated; response.Hostname != want {
		t.Errorf("expected %q once lookups are free, got %q", want, response.Hostname)
	}
	if cached, ok := srv.cache.get(netip.MustParseAddr(saturated)); !ok || cached.Hostname == "" {
		t.Error("expected the complete answer cached once lookups are free")
	}
}

// Many concurrent requests for fresh addresses never run more lookups at
// once than the cap, and each one is answered.
func TestReverseLookupCapHoldsUnderLoad(t *testing.T) {
	const limit, requests = 4, 32
	lookup := newBlockingLookup()
	srv := New(Config{LookupAddr: lookup.lookupAddr}, &testDB{}, NewCache(100))
	srv.lookups = make(chan struct{}, limit)
	h := srv.Handler()

	var wg sync.WaitGroup
	var answered atomic.Int32
	done := make(chan struct{}, requests)
	for i := range requests {
		ip := netip.AddrFrom4([4]byte{1, 1, 1, byte(i + 1)}).String()
		wg.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/json?ip="+ip, nil))
			if w.Code == http.StatusOK {
				answered.Add(1)
			}
			done <- struct{}{}
		})
	}

	// Every request over the cap returns while the others still hold their
	// lookups.
	for range requests - limit {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("requests over the cap waited on a reverse lookup")
		}
	}
	if n := lookup.inFlight.Load(); n != limit {
		t.Errorf("expected %d lookups in flight, got %d", limit, n)
	}
	close(lookup.release)
	wg.Wait()

	if most := lookup.most.Load(); most > limit {
		t.Errorf("expected at most %d lookups at once, got %d", limit, most)
	}
	if n := answered.Load(); n != requests {
		t.Errorf("expected %d answers with 200, got %d", requests, n)
	}
}

// A lookup that ran and found no name is a complete answer and is cached.
func TestEmptyHostnameIsCached(t *testing.T) {
	cfg := Config{LookupAddr: func(context.Context, netip.Addr) (string, error) { return "", nil }}
	srv := New(cfg, &testDB{}, NewCache(10))
	if status, _ := getJSON(t, srv.Handler(), "1.1.1.1"); status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if size := srv.cache.stats().Size; size != 1 {
		t.Errorf("expected the answer without a name cached, got %d entries", size)
	}
}
