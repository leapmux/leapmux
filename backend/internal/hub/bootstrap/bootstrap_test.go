package bootstrap_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

func setupStore(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
}

func TestRun_SkipsHubMode(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	// Hub mode (soloMode=false, devMode=false) should not create any orgs or users.
	err := bootstrap.Run(ctx, st, false, false)
	require.NoError(t, err)

	hasOrgs, err := st.Orgs().HasAny(ctx)
	require.NoError(t, err)
	assert.False(t, hasOrgs)

	hasUsers, err := st.Users().HasAny(ctx)
	require.NoError(t, err)
	assert.False(t, hasUsers)
}

func TestRun_SoloMode(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, true, false)
	require.NoError(t, err)

	org, err := st.Orgs().GetByName(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", org.Name)

	user, err := st.Users().GetByUsername(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.True(t, user.IsAdmin)
	assert.Empty(t, user.PasswordHash)
}

func TestRun_DevMode(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, false, true)
	require.NoError(t, err)

	org, err := st.Orgs().GetByName(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", org.Name)

	user, err := st.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.True(t, user.IsAdmin)

	// Dev mode should have a valid password hash.
	match, err := password.Verify(user.PasswordHash, "admin123")
	assert.NoError(t, err)
	assert.True(t, match)
}

func TestRun_Idempotent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, st, true, false)
	require.NoError(t, err)

	err = bootstrap.Run(ctx, st, true, false)
	require.NoError(t, err)

	orgs, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, orgs, 1)
}
