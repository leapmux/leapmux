package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupTestStore(t *testing.T) store.TestableStore {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRun_CleansUpOldRecords(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// Create a user + org.
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "testorg", IsPersonal: true})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: true,
	})
	require.NoError(t, err)

	// Soft-delete the user and org, backdate to 8 days ago.
	err = st.Users().Delete(ctx, userID)
	require.NoError(t, err)
	err = st.Orgs().SoftDelete(ctx, orgID)
	require.NoError(t, err)
	past := time.Now().UTC().Add(-8 * 24 * time.Hour)
	err = st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, userID, past)
	require.NoError(t, err)
	err = st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, past)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, st)

	// Verify hard-deleted.
	_, err = st.Users().GetByIDIncludeDeleted(ctx, userID)
	require.ErrorIs(t, err, store.ErrNotFound)

	_, err = st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRun_RetainsRecentlyDeleted(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// Create and soft-delete a user (recent, within retention).
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "testorg", IsPersonal: true})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: true,
	})
	require.NoError(t, err)
	err = st.Users().Delete(ctx, userID)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, st)

	// User should still exist (recently deleted, within 7-day retention).
	user, err := st.Users().GetByIDIncludeDeleted(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, user.DeletedAt)
}
