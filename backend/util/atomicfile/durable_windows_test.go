//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// observedMove captures one native move call.
type observedMove struct {
	from   *uint16
	to     *uint16
	flags  uint32
	result error
}

// TestDurableWindowsPublicationFlags observes the native call the
// durable replace makes: same-volume MoveFileExW with
// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH, retried on a
// sharing conflict, never falling back to a copy, and propagating a
// native failure.
func TestDurableWindowsPublicationFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, WriteFile(path, []byte("old"), 0o600))

	var mu sync.Mutex
	var calls []observedMove
	// The first call answers a sharing conflict; the second succeeds.
	conflicts := 0
	prev := moveFileExDurable
	moveFileExDurable = func(from, to *uint16, flags uint32) error {
		mu.Lock()
		defer mu.Unlock()
		var err error
		if conflicts == 0 {
			conflicts++
			err = windows.ERROR_SHARING_VIOLATION
		} else {
			err = prev(from, to, flags)
		}
		calls = append(calls, observedMove{from: from, to: to, flags: flags, result: err})
		return err
	}
	t.Cleanup(func() { moveFileExDurable = prev })

	require.NoError(t, WriteFileDurable(path, []byte("new"), 0o600))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, 2, "the sharing conflict is retried exactly once")
	for _, c := range calls {
		assert.Equal(t, uint32(windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH), c.flags,
			"the durable replace publishes write-through on the same volume")
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	// A native failure that is not a sharing conflict propagates at
	// once; there is no copy fallback to absorb it. The destination
	// keeps its previous whole document.
	calls = nil
	notFound := 0
	moveFileExDurable = func(from, to *uint16, flags uint32) error {
		notFound++
		return windows.ERROR_FILE_NOT_FOUND
	}
	err = WriteFileDurable(path, []byte("next"), 0o600)
	require.Error(t, err)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "new", string(data), "the destination keeps its whole previous document")
	assert.Equal(t, 1, notFound, "a non-conflict native failure is returned without a retry")
}

func TestCreateFileDurableWindowsUsesExclusiveWriteThrough(t *testing.T) {
 original := moveFileExDurable
 t.Cleanup(func() { moveFileExDurable = original })
 var flags []uint32
 moveFileExDurable = func(from, to *uint16, option uint32) error {
  flags = append(flags, option)
  return original(from, to, option)
 }
 path := filepath.Join(t.TempDir(), "ring")
 require.NoError(t, CreateFileDurable(path, []byte("original ring"), 0o600))
 require.ErrorIs(t, CreateFileDurable(path, []byte("another ring"), 0o600), os.ErrExist)
 require.Equal(t, []uint32{windows.MOVEFILE_WRITE_THROUGH, windows.MOVEFILE_WRITE_THROUGH}, flags)
 data, err := os.ReadFile(path)
 require.NoError(t, err)
 require.Equal(t, "original ring", string(data))
}
