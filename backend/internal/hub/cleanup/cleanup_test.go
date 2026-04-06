package cleanup

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupTestDB(t *testing.T) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(sqlDB))
	return sqlDB, gendb.New(sqlDB)
}

func TestRun_CleansUpOldRecords(t *testing.T) {
	sqlDB, q := setupTestDB(t)
	ctx := context.Background()

	// Create a user + org.
	orgID := id.Generate()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "testorg", IsPersonal: 1})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: 1,
	})
	require.NoError(t, err)

	// Soft-delete the user and org, backdate to 8 days ago.
	err = q.DeleteUser(ctx, userID)
	require.NoError(t, err)
	err = q.SoftDeleteOrg(ctx, orgID)
	require.NoError(t, err)
	past := time.Now().UTC().Add(-8 * 24 * time.Hour)
	_, err = sqlDB.ExecContext(ctx, "UPDATE users SET deleted_at = ? WHERE id = ?", past, userID)
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, "UPDATE orgs SET deleted_at = ? WHERE id = ?", past, orgID)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, q)

	// Verify hard-deleted.
	_, err = q.GetUserByIDIncludeDeleted(ctx, userID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	_, err = q.GetOrgByIDIncludeDeleted(ctx, orgID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestRun_RetainsRecentlyDeleted(t *testing.T) {
	_, q := setupTestDB(t)
	ctx := context.Background()

	// Create and soft-delete a user (recent, within retention).
	orgID := id.Generate()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "testorg", IsPersonal: 1})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = q.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: 1,
	})
	require.NoError(t, err)
	err = q.DeleteUser(ctx, userID)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, q)

	// User should still exist (recently deleted, within 7-day retention).
	user, err := q.GetUserByIDIncludeDeleted(ctx, userID)
	require.NoError(t, err)
	require.True(t, user.DeletedAt.Valid)
}
