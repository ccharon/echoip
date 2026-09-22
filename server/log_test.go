package server

import (
	"log/slog"
	"strings"
	"testing"
)

// TestMain keeps the log out of the test output. A test that checks the log
// swaps in its own logger with captureLog.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	m.Run()
}

// captureLog sends the log to the returned builder for the rest of the test.
func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var logged strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logged
}
