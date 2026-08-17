package testutil

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
)

// LogBuffer holds the log lines that CaptureDefaultLogger captured.
//
// It is safe for concurrent use, which is the property it exists to supply:
// the test goroutine can call String while the code under test still writes
// log lines through slog from another goroutine. A require.Eventually loop
// that waits for a teardown line is exactly this shape, and a plain
// bytes.Buffer makes it a data race. One mutex guards both the write and the
// read.
//
// This is a different concern from the process-global swap that
// CaptureDefaultLogger does. A safe buffer does not make the swap safe, so the
// t.Parallel limit below still applies.
type LogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends the log line. The slog handler calls it from whichever
// goroutine emits the line.
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns every line captured so far. It is safe to call while the code
// under test still logs.
func (b *LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset discards the lines captured so far. A test calls it to drop what the
// setup logged, so a later assertion reads only what the step under test wrote.
func (b *LogBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// CaptureDefaultLogger redirects slog's default logger into a buffer for the
// duration of the test and restores it afterwards.
//
// It exists because the LEVEL a line is reported at is itself behaviour worth
// pinning -- "an ordinary disconnect must not warn" is a real contract, and the
// default logger is the only place it is observable. The handler admits Debug so
// a test can assert a line was demoted rather than deleted.
//
// Not parallel-safe: it swaps a process-global, so tests using it must not call
// t.Parallel(). Go never overlaps a sequential test with a parallel one, so a
// test that obeys that rule captures its own lines only. The limit covers the
// swap. The returned LogBuffer is safe to read while the code under test writes
// to it.
func CaptureDefaultLogger(t *testing.T) *LogBuffer {
	t.Helper()
	buf := &LogBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
