package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newSelectorStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	return st
}

func newSelectorUser(t *testing.T, st store.Store, username string) *store.User {
	t.Helper()
	hash, err := password.Hash("testpass123")
	require.NoError(t, err)
	user, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username: username, PasswordHash: hash, DisplayName: username, FirstCredentialExempt: true,
	})
	require.NoError(t, err)
	return user
}

// ResolveUserSelector holds the one (id | username) rule for BOTH the online
// admin RPCs and the offline `leapmux recover` verbs. It returns the two rule
// violations as sentinels so each surface can word them for its own audience
// — the RPC gives proto fields, the CLI gives flags.
func TestResolveUserSelector(t *testing.T) {
	st := newSelectorStore(t)
	created := newSelectorUser(t, st, "alice")

	t.Run("resolves by id", func(t *testing.T) {
		user, err := service.ResolveUserSelector(context.Background(), st, created.ID, "")
		require.NoError(t, err)
		assert.Equal(t, created.ID, user.ID)
	})

	t.Run("resolves by username", func(t *testing.T) {
		user, err := service.ResolveUserSelector(context.Background(), st, "", "alice")
		require.NoError(t, err)
		assert.Equal(t, created.ID, user.ID)
	})

	t.Run("refuses neither", func(t *testing.T) {
		_, err := service.ResolveUserSelector(context.Background(), st, "", "")
		assert.ErrorIs(t, err, service.ErrNoUserSelector)
	})

	t.Run("refuses both", func(t *testing.T) {
		_, err := service.ResolveUserSelector(context.Background(), st, created.ID, "alice")
		assert.ErrorIs(t, err, service.ErrAmbiguousUserSelector)
	})

	// A lookup failure passes through verbatim, so errors.Is still
	// classifies it at either surface.
	t.Run("a miss stays store.ErrNotFound", func(t *testing.T) {
		_, err := service.ResolveUserSelector(context.Background(), st, "", "nobody")
		assert.ErrorIs(t, err, store.ErrNotFound)

		_, err = service.ResolveUserSelector(context.Background(), st, "00000000000000000000000000", "")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
