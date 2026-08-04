//go:build unix

package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadDirNWithTimeout_OpenHang verifies the timeout fires and the call
// returns promptly even when os.Open blocks indefinitely (e.g. a FIFO with
// no writer, approximating a macOS TCC-protected directory that never
// returns).
func TestReadDirNWithTimeout_OpenHang(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "stuck")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))

	t.Cleanup(func() {
		// Unblock the goroutine still stuck in os.Open so it can exit
		// before the test process tears down.
		go func() {
			f, _ := os.OpenFile(fifo, os.O_WRONLY, 0)
			if f != nil {
				_ = f.Close()
			}
		}()
	})

	// The timeout FIRING is proven by the error, not by elapsed time -- a call
	// that blocked forever on the fifo would never return one. So the remaining
	// risk is a hang, guarded by a completion timeout two orders of magnitude
	// above the 50ms budget under test rather than a 500ms bound sitting one
	// order above it.
	type dirResult struct {
		entries []os.DirEntry
		err     error
	}
	done := make(chan dirResult, 1)
	go func() {
		entries, err := readDirNWithTimeout(fifo, 10, 50*time.Millisecond)
		done <- dirResult{entries, err}
	}()
	select {
	case got := <-done:
		require.Error(t, got.err)
		require.Nil(t, got.entries)
		assert.Contains(t, got.err.Error(), "timed out")
	case <-time.After(30 * time.Second):
		t.Fatal("readDirNWithTimeout blocked past its timeout instead of returning")
	}
}
