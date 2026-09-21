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
