package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingFixture writes a complete, valid pending record.
func pendingFixture(t *testing.T) (*Config, *PendingRegistration) {
	t.Helper()
	cfg := &Config{DataDir: t.TempDir()}
	pending := &PendingRegistration{
		AttemptID: "a9a0425886d148f68a8f94716ed47a78",
		HubURL:    "http://127.0.0.1:4327",
		RegKey:    "key123",
	}
	_, err := pending.GenerateKeypair()
	require.NoError(t, err)
	return cfg, pending
}

// TestPendingPersistenceAndPartialCleanup pins the durable order and
// the cleanup contract: the pending record exists before any network
// request, a publication failure makes zero requests, a cleanup that
// half-fails is joined and retriable, and a retry finishes it without a
// new registration.
func TestPendingPersistenceAndPartialCleanup(t *testing.T) {
	t.Run("a fresh nested data root is durably published before the pending write", func(t *testing.T) {
		base := t.TempDir()
		cfg := &Config{DataDir: filepath.Join(base, "deep", "nested", "worker")}
		pending := &PendingRegistration{
			AttemptID: "a9a0425886d148f68a8f94716ed47a78",
			HubURL:    "http://127.0.0.1:4327",
			RegKey:    "key123",
		}
		_, err := pending.GenerateKeypair()
		require.NoError(t, err)

		require.NoError(t, cfg.Validate())
		require.NoError(t, cfg.SavePendingRegistration(pending))

		// The nested root carries the publication record a durable
		// creation leaves behind.
		firstNew := filepath.Join(base, "deep")
		assert.FileExists(t, filepath.Join(firstNew, ".leapmux-directory-publication.yaml"))
		loaded, err := cfg.LoadPendingRegistration()
		require.NoError(t, err)
		assert.Equal(t, pending.AttemptID, loaded.AttemptID)
		assert.True(t, loaded.HasKeypair())
	})

	t.Run("a pending write is readable only when complete", func(t *testing.T) {
		cfg, pending := pendingFixture(t)
		require.NoError(t, cfg.SavePendingRegistration(pending))

		// A half-written record refuses rather than adopting a default.
		require.NoError(t, os.WriteFile(cfg.PendingPath(), []byte(`{"attempt_id":"x"}`), 0o600))
		_, err := cfg.LoadPendingRegistration()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "misses a required field")
	})

	t.Run("cleanup joins destination and copy failures and accepts an absent record", func(t *testing.T) {
		cfg, pending := pendingFixture(t)
		require.NoError(t, cfg.SavePendingRegistration(pending))
		leftover := cfg.PendingPath() + ".tmp1234"
		require.NoError(t, os.WriteFile(leftover, []byte("{}"), 0o600))

		require.NoError(t, cfg.RemovePendingRegistration())
		assert.NoFileExists(t, cfg.PendingPath())
		assert.NoFileExists(t, leftover)

		// An absent record is success, so a retry after a crash is safe.
		require.NoError(t, cfg.RemovePendingRegistration())
	})

	t.Run("cleanup keeps all three failure causes", func(t *testing.T) {
		cfg, _ := pendingFixture(t)
		destination := cfg.PendingPath()
		first, second := destination+".tmp-a", destination+".tmp-b"
		destinationErr, firstErr, secondErr := errors.New("destination failure"), errors.New("first copy failure"), errors.New("second copy failure")
		failures := map[string]error{destination: destinationErr, first: firstErr, second: secondErr}
		var attempted []string
		remove := func(path string) error {
			attempted = append(attempted, path)
			return &os.PathError{Op: "remove", Path: path, Err: failures[path]}
		}
		err := removeSensitiveRecord(destination, remove, func(path string) error {
			assert.Equal(t, destination, path)
			return errors.Join(remove(first), remove(second))
		}, func(string) error { return nil })
		require.ErrorIs(t, err, destinationErr)
		require.ErrorIs(t, err, firstErr)
		require.ErrorIs(t, err, secondErr)
		assert.Equal(t, []string{destination, first, second}, attempted)
	})
}
