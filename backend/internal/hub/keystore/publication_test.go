package keystore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyRingMutationsShareOnePersistentLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")
	original, err := LoadOrGenerate(path)
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		completed <- withKeyRingMutation(path, func(string) error { close(entered); <-release; return nil })
	}()
	ctx := testutil.DeadlineContext(t)
	released, joined := false, false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		if !joined {
			cleanupErr := <-completed
			require.NoError(t, cleanupErr)
		}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = RotateKey(path)
	require.ErrorIs(t, err, errKeyRingMutationBusy)
	require.ErrorIs(t, RemoveKey(path, 1), errKeyRingMutationBusy)
	require.ErrorIs(t, RegeneratePepper(path), errKeyRingMutationBusy)
	loaded, err := LoadOrGenerate(path)
	require.NoError(t, err, "immutable loading must not need the mutation lock")
	assert.Equal(t, original.Pepper(), loaded.Pepper())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	close(release)
	released = true
	// Join the held mutation before taking its persistent lock again.
	result := <-completed
	joined = true
	require.NoError(t, result)
	version, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), version)
	reloaded, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, original.Pepper(), reloaded.Pepper())
	assert.FileExists(t, path+".lock")
}

func TestLoadOrGeneratePublishesOneCompleteInitialRing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "directory", "ring")
	const count = 16
	results := make(chan struct {
		ring *Keystore
		err  error
	}, count)
	start := make(chan struct{})
	for range count {
		go func() {
			<-start
			ring, err := LoadOrGenerate(path)
			results <- struct {
				ring *Keystore
				err  error
			}{ring, err}
		}()
	}
	close(start)
	successes := 0
	var winner *Keystore
	for range count {
		result := <-results
		if result.err != nil {
			require.ErrorIs(t, result.err, errKeyRingMutationBusy)
			continue
		}
		successes++
		if winner == nil {
			winner = result.ring
		} else {
			assert.Equal(t, winner.keys, result.ring.keys)
			assert.Equal(t, winner.Pepper(), result.ring.Pepper())
		}
	}
	require.Positive(t, successes)
	final, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, winner.keys, final.keys)
	assert.Equal(t, winner.Pepper(), final.Pepper())
	assert.Equal(t, []uint32{1}, final.Versions())
}

func TestKeyRingMutationPreservesLiteralRelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join("~", "private", "ring")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, path))
	version, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), version)
}
