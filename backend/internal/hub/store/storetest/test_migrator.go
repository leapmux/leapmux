package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testMigrator(t *testing.T) {
	t.Run("current version", func(t *testing.T) {
		st := s.NewStore(t)
		v, err := st.Migrator().CurrentVersion(ctx)
		require.NoError(t, err)
		// After NewStore (which should have migrated), current should equal latest.
		assert.Equal(t, st.Migrator().LatestVersion(), v)
	})

	t.Run("latest version is positive", func(t *testing.T) {
		st := s.NewStore(t)
		assert.Greater(t, st.Migrator().LatestVersion(), int64(0))
	})

	t.Run("migrate is idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		// Running Migrate again should be a no-op.
		err := st.Migrator().Migrate(ctx)
		require.NoError(t, err)

		v, err := st.Migrator().CurrentVersion(ctx)
		require.NoError(t, err)
		assert.Equal(t, st.Migrator().LatestVersion(), v)
	})

	t.Run("migrate to current version", func(t *testing.T) {
		st := s.NewStore(t)
		latest := st.Migrator().LatestVersion()

		// MigrateTo the current (latest) version should be a no-op.
		err := st.Migrator().MigrateTo(ctx, latest)
		require.NoError(t, err)

		v, err := st.Migrator().CurrentVersion(ctx)
		require.NoError(t, err)
		assert.Equal(t, latest, v)
	})

	t.Run("migrate to zero", func(t *testing.T) {
		st := s.NewStore(t)

		// MigrateTo(0) should either rollback all migrations or return
		// ErrRollbackNotSupported — both are valid depending on backend.
		err := st.Migrator().MigrateTo(ctx, 0)
		if err != nil {
			assert.ErrorIs(t, err, store.ErrRollbackNotSupported)
		} else {
			v, err := st.Migrator().CurrentVersion(ctx)
			require.NoError(t, err)
			assert.Equal(t, int64(0), v)
		}
	})
}
