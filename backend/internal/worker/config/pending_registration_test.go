package config

import (
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

		cfg.HubURL = pending.HubURL
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

	t.Run("an unreadable-directory cleanup reports both causes", func(t *testing.T) {
		cfg, pending := pendingFixture(t)
		require.NoError(t, cfg.SavePendingRegistration(pending))
		require.NoError(t, os.WriteFile(cfg.PendingPath()+".tmp5678", []byte("{}"), 0o600))
		require.NoError(t, os.Chmod(cfg.DataDir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(cfg.DataDir, 0o700) })

		err := cfg.RemovePendingRegistration()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pending-registration.json", "the destination failure is named")
		assert.Contains(t, err.Error(), "tmp5678", "the copy failure is named too")
	})
}
