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

	start := time.Now()
	entries, err := readDirNWithTimeout(fifo, 10, 50*time.Millisecond)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Nil(t, entries)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 500*time.Millisecond, "readDirN blocked past its timeout")
}
