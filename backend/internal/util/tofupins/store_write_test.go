package tofupins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteStateChecksForCancellationAfterSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	original := []byte(`{"pins":{}}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	store, err := Open(Options{Path: path, DirMode: 0o700, FileMode: 0o600})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err = store.writeState(ctx, func() ([]byte, error) {
		cancel()
		return []byte(`{"pins":{},"removal_revision":1}`), nil
	})

	assert.ErrorIs(t, err, context.Canceled)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, after)
}
