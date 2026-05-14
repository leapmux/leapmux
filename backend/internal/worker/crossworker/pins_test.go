package crossworker_test

import (
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
	require.NoError(t, store.Verify("target-worker", []byte("x"), []byte("m"), []byte("s")))
	pins := store.List()
	require.Contains(t, pins, "target-worker")
}

func TestWorkerPins_MismatchRefusesUntilCleared(t *testing.T) {
	dir := t.TempDir()
	store, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	require.NoError(t, store.Verify("target-worker", []byte("x"), []byte("m"), []byte("s")))

	// Mismatch: must error AND must keep refusing until explicit clear.
	err = store.Verify("target-worker", []byte("x-new"), []byte("m"), []byte("s"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")

	// Even retrying with the same wrong keys must still fail.
	err = store.Verify("target-worker", []byte("x-new"), []byte("m"), []byte("s"))
	require.Error(t, err)

	// After explicit Remove, the new keys re-pin.
	require.NoError(t, store.Remove("target-worker"))
	require.NoError(t, store.Verify("target-worker", []byte("x-new"), []byte("m"), []byte("s")))
}

func TestWorkerPins_PersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	first, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	require.NoError(t, first.Verify("worker-A", []byte("x"), []byte("m"), []byte("s")))

	second, err := crossworker.NewPinStore(dir)
	require.NoError(t, err)
	assert.Contains(t, second.List(), "worker-A")
}
