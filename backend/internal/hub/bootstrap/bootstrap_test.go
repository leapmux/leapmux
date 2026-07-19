package bootstrap_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
)

func setupStore(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
}

// TestRun_SkipsNonSolo asserts that hub/dev mode is a no-op — the first admin
// user must be registered via the /setup flow, not auto-created.
func TestRun_SkipsNonSolo(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, false)
	require.NoError(t, err)

	hasUsers, err := st.Users().HasAny(ctx)
	require.NoError(t, err)
	assert.False(t, hasUsers)
}

func TestRun_SoloMode(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, true)
	require.NoError(t, err)

	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	require.NoError(t, err)
	assert.Equal(t, usernames.Solo, user.Username)
	assert.True(t, user.IsAdmin)
	assert.Empty(t, user.PasswordHash)

	// The personal org is created alongside the user and carries the
	// username as its name.
	org, err := st.Orgs().GetByID(ctx, user.OrgID)
	require.NoError(t, err)
	assert.Equal(t, usernames.Solo, org.Name)
}

func TestRun_Idempotent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, true)
	require.NoError(t, err)

	err = bootstrap.Run(ctx, st, true)
	require.NoError(t, err)

	users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, users, 1)
}
