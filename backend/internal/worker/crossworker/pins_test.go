package crossworker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

func TestWorkerPins_AutoPinOnFirstContact(t *testing.T) {
	store, err := crossworker.NewPinStore(t.TempDir())
	require.NoError(t, err)
	// Worker-side TOFU: first contact must succeed silently — agents
	// can't answer interactive prompts, so the design is to auto-pin
	// and rely on user-driven removal for re-key.
	ctx := context.Background()
	commit, err := store.Resolve(ctx, "target-worker", []byte("x"), []byte("m"), []byte("s"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))
	pins, err := store.Snapshot(ctx)
	require.NoError(t, err)
	require.Contains(t, pins, "target-worker")
}

// Resolve decides; the commit publishes. A channel that never proves its
// session records no pin.
func TestWorkerPins_ResolveAloneRecordsNoPin(t *testing.T) {
	store, err := crossworker.NewPinStore(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	_, err = store.Resolve(ctx, "target-worker", []byte("x"), []byte("m"), []byte("s"))
	require.NoError(t, err)

	pins, err := store.Snapshot(ctx)
	require.NoError(t, err)
	assert.Empty(t, pins)
}

func TestWorkerPins_MismatchRefusesUntilCleared(t *testing.T) {
	dir := t.TempDir()
	store, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	ctx := context.Background()
	commit, err := store.Resolve(ctx, "target-worker", []byte("x"), []byte("m"), []byte("s"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	// Mismatch: must error AND must keep refusing until explicit clear.
	_, err = store.Resolve(ctx, "target-worker", []byte("x-new"), []byte("m"), []byte("s"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")

	// Even retrying with the same wrong keys must still fail.
	_, err = store.Resolve(ctx, "target-worker", []byte("x-new"), []byte("m"), []byte("s"))
	require.Error(t, err)

	// After explicit Remove, the new keys re-pin.
	require.NoError(t, store.Remove(ctx, "target-worker"))
	commit, err = store.Resolve(ctx, "target-worker", []byte("x-new"), []byte("m"), []byte("s"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))
}

// Removing one target's pin must keep the pins of unrelated targets: the
// remove verb identifies exactly one target worker id.
func TestWorkerPins_RemovePreservesUnrelatedPins(t *testing.T) {
	store, err := crossworker.NewPinStore(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	for _, id := range []string{"worker-A", "worker-B"} {
		commit, err := store.Resolve(ctx, id, []byte("x"), []byte("m"), []byte("s"))
		require.NoError(t, err)
		require.NoError(t, commit(ctx))
	}

	require.NoError(t, store.Remove(ctx, "worker-A"))
	pins, err := store.Snapshot(ctx)
	require.NoError(t, err)
	require.Contains(t, pins, "worker-B")
	assert.NotContains(t, pins, "worker-A")
}

// A document that cannot be parsed must surface as a failure on List, not
// read as an empty pin map: `cross-worker-pins list` and `show` share this
// call, and an empty answer would claim no pin is recorded.
func TestWorkerPins_ListSurfacesAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	store, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cross_worker_pins.json"), []byte("{ not json"), 0o600))

	_, err = store.Snapshot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pins")
}

func TestWorkerPins_PersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	commit, err := first.Resolve(ctx, "worker-A", []byte("x"), []byte("m"), []byte("s"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	second, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	pins, err := second.Snapshot(ctx)
	require.NoError(t, err)
	assert.Contains(t, pins, "worker-A")
}
