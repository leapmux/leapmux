//go:build unix

package amp

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A settings path that points at a FIFO would block the read for ever, with
// sendMu held, and the agent would take no message. The reader refuses any file
// that is not regular before it opens it.
func TestReadUserSettingsRefusesAFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := readSettingsFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		assert.ErrorContains(t, err, "not a regular file")
	case <-time.After(30 * time.Second):
		t.Fatal("the read of a FIFO blocked")
	}
}
