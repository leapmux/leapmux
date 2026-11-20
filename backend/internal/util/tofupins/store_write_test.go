package tofupins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
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

func TestTransactionWaitHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	store, err := Open(Options{Path: path, DirMode: 0o700, FileMode: 0o600})
	require.NoError(t, err)
	store.transaction <- struct{}{}
	defer func() { <-store.transaction }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, snapshotErr := store.Snapshot(ctx)
		done <- snapshotErr
	}()

	select {
	case snapshotErr := <-done:
		assert.ErrorIs(t, snapshotErr, context.Canceled)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("a canceled transaction wait did not return")
	}
}
