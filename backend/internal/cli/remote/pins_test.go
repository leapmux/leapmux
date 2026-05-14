package remote_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/remote"
)

const testHub = "https://test-hub.example"

func newPinsForTest(t *testing.T) *remote.PinStore {
	t.Helper()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", t.TempDir())
	store, err := remote.NewPinStore(testHub)
	require.NoError(t, err)
	return store
}

func TestPins_TOFUFirstSeenRecordsPin(t *testing.T) {
	store := newPinsForTest(t)
	require.NoError(t, store.Verify("worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa")))

	pins := store.List()
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-1", pins[0].WorkerID)
}

func TestPins_MismatchAborts(t *testing.T) {
	store := newPinsForTest(t)
	require.NoError(t, store.Verify("worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa")))

	// Different key bytes for the same worker_id → mismatch.
	err := store.Verify("worker-1", []byte("x25519-new"), []byte("mlkem"), []byte("slhdsa"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
}

func TestPins_RemoveAllowsRePin(t *testing.T) {
	store := newPinsForTest(t)
	require.NoError(t, store.Verify("worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa")))

	require.NoError(t, store.Remove("worker-1"))
	assert.Empty(t, store.List())

	// New keys must now succeed (TOFU re-records).
	require.NoError(t, store.Verify("worker-1", []byte("x25519-new"), []byte("mlkem-new"), []byte("slhdsa-new")))
	assert.Len(t, store.List(), 1)
}

func TestPins_RemoveIdempotent(t *testing.T) {
	store := newPinsForTest(t)
	require.NoError(t, store.Remove("never-seen"))
}

func TestPins_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	first, err := remote.NewPinStore(testHub)
	require.NoError(t, err)
	require.NoError(t, first.Verify("worker-1", []byte("x25519"), []byte("mlkem"), []byte("slhdsa")))

	second, err := remote.NewPinStore(testHub)
	require.NoError(t, err)
	pins := second.List()
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-1", pins[0].WorkerID)
}
