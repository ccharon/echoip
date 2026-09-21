package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListenAndServeShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- testServer().ListenAndServe(ctx, "127.0.0.1:0") }()

	// Give the listener a moment before asking it to stop.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the shutdown")
	}
}

// The page is built into the binary, so it is served without any file beside
// the executable.
func TestBrowserPage(t *testing.T) {
	log.SetOutput(io.Discard)
	s := httptest.NewServer(New(Config{}, &testDb{}, NewCache(0)).Handler())

	out, status, err := httpGet(s.URL, "", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Errorf("expected the browser page, got %q", out)
	}
}

// The limit is maxHeaderBytes plus the 4 KiB net/http reads for its buffer, so
// a request is refused above 12 KiB rather than above 8.
func TestHeaderLimit(t *testing.T) {
	srv := New(Config{}, &testDb{}, NewCache(0))
	s := httptest.NewUnstartedServer(srv.Handler())
	s.Config.MaxHeaderBytes = maxHeaderBytes
	s.Start()
	defer s.Close()

	status := func(size int) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, s.URL+"/ip", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Filler", strings.Repeat("a", size))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}

	if got := status(maxHeaderBytes); got != 200 {
		t.Errorf("Expected a header of %d bytes to pass, got %d", maxHeaderBytes, got)
	}
	if got := status(maxHeaderBytes + 8<<10); got != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("Expected 431 for an oversized header, got %d", got)
	}
}
