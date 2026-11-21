package tofupins

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

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

	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &observedDoneContext{Context: base, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, snapshotErr := store.Snapshot(ctx)
		done <- snapshotErr
	}()

	select {
	case <-ctx.observed:
		cancel()
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the second transaction did not wait for the in-process owner")
	}
	select {
	case snapshotErr := <-done:
		assert.ErrorIs(t, snapshotErr, context.Canceled)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("a canceled transaction wait did not return")
	}
}
