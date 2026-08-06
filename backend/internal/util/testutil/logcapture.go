package testutil

import (
	"bytes"
	"log/slog"
	"testing"
)

// CaptureDefaultLogger redirects slog's default logger into a buffer for the
// duration of the test and restores it afterwards.
//
// It exists because the LEVEL a line is reported at is itself behaviour worth
// pinning -- "an ordinary disconnect must not warn" is a real contract, and the
// default logger is the only place it is observable. The handler admits Debug so
// a test can assert a line was demoted rather than deleted.
//
// Not parallel-safe: it swaps a process-global, so tests using it must not call
// t.Parallel().
func CaptureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
