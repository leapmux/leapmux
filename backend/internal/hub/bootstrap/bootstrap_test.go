package bootstrap_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
)

func setupDB(t *testing.T) *gendb.Queries {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	return gendb.New(sqlDB)
}

func TestRun_CreatesOrgAndAdmin(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, q, false)
	require.NoError(t, err)

	// Verify org was created.
	org, err := q.GetOrgByName(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", org.Name)

	// Verify admin user was created.
	user, err := q.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.Equal(t, int64(1), user.IsAdmin)

	// Verify password hash is valid Argon2id.
	match, err := password.Verify(user.PasswordHash, "admin")
	assert.NoError(t, err)
	assert.True(t, match)
}

func TestRun_SoloMode(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, q, true)
	require.NoError(t, err)

	// Verify org was created with "solo" name.
	org, err := q.GetOrgByName(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", org.Name)

	// Verify user was created with "solo" username.
	user, err := q.GetUserByUsername(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, "solo", user.Username)
	assert.Equal(t, org.ID, user.OrgID)
	assert.Equal(t, int64(1), user.IsAdmin)

	// Verify password hash is empty in solo mode.
	assert.Empty(t, user.PasswordHash)
}

func TestRun_Idempotent(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()

	err := bootstrap.Run(ctx, q, false)
	require.NoError(t, err)

	// Second run should be a no-op (org already exists).
	err = bootstrap.Run(ctx, q, false)
	require.NoError(t, err)

	// Should still have exactly one org.
	count, err := q.CountOrgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
