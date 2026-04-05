package bootstrap_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
)

func setupDB(t *testing.T) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	return sqlDB, gendb.New(sqlDB)
}

func TestRun_SkipsHubMode(t *testing.T) {
	sqlDB, q := setupDB(t)
	ctx := context.Background()

	// Hub mode (soloMode=false, devMode=false) should not create any orgs or users.
	err := bootstrap.Run(ctx, sqlDB, q, false, false)
	require.NoError(t, err)

	orgCount, err := q.CountOrgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), orgCount)

	userCount, err := q.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), userCount)
}

func TestRun_SoloMode(t *testing.T) {
	sqlDB, q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, sqlDB, q, true, false)
	require.NoError(t, err)

	org, err := q.GetOrgByName(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", org.Name)

	user, err := q.GetUserByUsername(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.Equal(t, int64(1), user.IsAdmin)
	assert.Empty(t, user.PasswordHash)
}

func TestRun_DevMode(t *testing.T) {
	sqlDB, q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, sqlDB, q, false, true)
	require.NoError(t, err)

	org, err := q.GetOrgByName(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", org.Name)

	user, err := q.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.Equal(t, int64(1), user.IsAdmin)

	// Dev mode should have a valid password hash.
	match, err := password.Verify(user.PasswordHash, "admin123")
	assert.NoError(t, err)
	assert.True(t, match)
}

func TestRun_Idempotent(t *testing.T) {
	sqlDB, q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, sqlDB, q, true, false)
	require.NoError(t, err)

	err = bootstrap.Run(ctx, sqlDB, q, true, false)
	require.NoError(t, err)

	count, err := q.CountOrgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
