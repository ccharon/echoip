package maxmind

import (
	"log/slog"
	"testing"
)

// TestMain keeps the log out of the test output.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	m.Run()
}
