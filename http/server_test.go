package http

import (
	"context"
	"errors"
	"io"
	"log"
	stdhttp "net/http"
	"os"
	"path/filepath"
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
		if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the shutdown")
	}
}

func TestBrowserPageDisabledOnBrokenTemplate(t *testing.T) {
	log.SetOutput(io.Discard)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("{{ .Unclosed "), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{TemplateDir: dir}, &testDb{}, NewCache(0))

	if s.template != nil {
		t.Error("expected no template after a parse error")
	}
}
