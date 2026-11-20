package control_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/control"
)

const testHub = "https://test-hub.example"

func newPinsForTest(t *testing.T) *control.PinStore {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	store, err := control.NewPinStore(testHub)
	require.NoError(t, err)
	return store
}

func TestPins_TOFUFirstSeenRecordsPin(t *testing.T) {
	store := newPinsForTest(t)
	ctx := context.Background()
	commit, err := store.Resolve(ctx, "worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	pins, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-1", pins[0].WorkerID)
}

// Resolve decides; the commit publishes. An open that never proves its
// session (handshake or Ping failure) records no pin.
func TestPins_ResolveAloneRecordsNoPin(t *testing.T) {
	store := newPinsForTest(t)
	ctx := context.Background()
	_, err := store.Resolve(ctx, "worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)

	pins, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, pins)
}

func TestPins_MismatchAborts(t *testing.T) {
	store := newPinsForTest(t)
	ctx := context.Background()
	commit, err := store.Resolve(ctx, "worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	// Different key bytes for the same worker_id → mismatch.
	_, err = store.Resolve(ctx, "worker-1", []byte("x25519-new"), []byte("mlkem"), []byte("slhdsa"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
	// The mismatch hint points the user at the control command to clear
	// the pin, so a rename that forgets the production template fails here.
	assert.Contains(t, err.Error(), "leapmux control worker pins remove --worker-id=worker-1")
	assert.NotContains(t, err.Error(), "leapmux remote")
}

func TestPins_RemoveAllowsRePin(t *testing.T) {
	store := newPinsForTest(t)
	ctx := context.Background()
	commit, err := store.Resolve(ctx, "worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	require.NoError(t, store.Remove(ctx, "worker-1"))
	pins, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, pins)

	// New keys must now succeed (TOFU re-records).
	commit, err = store.Resolve(ctx, "worker-1", []byte("x25519-new"), []byte("mlkem-new"), []byte("slhdsa-new"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))
	pins, err = store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, pins, 1)
}

func TestPins_RemoveIdempotent(t *testing.T) {
	store := newPinsForTest(t)
	require.NoError(t, store.Remove(context.Background(), "never-seen"))
}

// Removing one worker's pin must not drop the pins of unrelated workers:
// `worker pins remove --worker-id=A` is how a user clears one mismatch.
func TestPins_RemovePreservesUnrelatedPins(t *testing.T) {
	store := newPinsForTest(t)
	ctx := context.Background()
	for _, id := range []string{"worker-1", "worker-2"} {
		commit, err := store.Resolve(ctx, id, []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
		require.NoError(t, err)
		require.NoError(t, commit(ctx))
	}

	require.NoError(t, store.Remove(ctx, "worker-1"))
	pins, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-2", pins[0].WorkerID)
}

// A document that cannot be parsed must surface as a failure on List,
// not read as an empty pin set: `worker pins list` and `worker pins show`
// share this call, and an empty answer would claim no pin is recorded.
func TestPins_ListSurfacesAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	store, err := control.NewPinStore(testHub)
	require.NoError(t, err)
	host, err := control.HubHost(testHub)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, host, "pins.json"), []byte("{ not json"), 0o644))

	_, err = store.List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pins")
}

func TestPins_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	ctx := context.Background()

	first, err := control.NewPinStore(testHub)
	require.NoError(t, err)
	commit, err := first.Resolve(ctx, "worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)
	require.NoError(t, commit(ctx))

	second, err := control.NewPinStore(testHub)
	require.NoError(t, err)
	pins, err := second.List(ctx)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-1", pins[0].WorkerID)
}
