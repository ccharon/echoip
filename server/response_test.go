package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
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
