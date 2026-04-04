package auth_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupDB(t *testing.T) *gendb.Queries {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	return gendb.New(sqlDB)
}

func createTestUser(t *testing.T, q *gendb.Queries) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()

	orgID = id.Generate()
	if err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "test-org"}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	hash, err := password.Hash("password123")
	require.NoError(t, err)

	userID = id.Generate()
	if err := q.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		IsAdmin:      1,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return orgID, userID
}

func TestLogin_Success(t *testing.T) {
	q := setupDB(t)
	orgID, userID := createTestUser(t, q)
	ctx := context.Background()

	token, user, _, err := auth.Login(ctx, q, "testuser", "password123")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, userID, user.ID)
	assert.Equal(t, orgID, user.OrgID)
}

func TestLogin_InvalidPassword(t *testing.T) {
	q := setupDB(t)
	createTestUser(t, q)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, q, "testuser", "wrongpassword")
	require.Error(t, err)
}

func TestLogin_UnknownUser(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, q, "nonexistent", "password")
	require.Error(t, err)
}

func TestLogin_HashUnchangedAfterLogin(t *testing.T) {
	q := setupDB(t)
	createTestUser(t, q)
	ctx := context.Background()

	user, err := q.GetUserByUsername(ctx, "testuser")
	require.NoError(t, err)
	originalHash := user.PasswordHash

	_, _, _, err = auth.Login(ctx, q, "testuser", "password123")
	require.NoError(t, err)

	user, err = q.GetUserByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.Equal(t, originalHash, user.PasswordHash, "argon2id hash should not change after login")
}

func TestValidateToken_Success(t *testing.T) {
	q := setupDB(t)
	createTestUser(t, q)
	ctx := context.Background()

	token, _, _, err := auth.Login(ctx, q, "testuser", "password123")
	require.NoError(t, err)

	info, err := auth.ValidateToken(ctx, q, token)
	require.NoError(t, err)
	assert.Equal(t, "testuser", info.Username)
	assert.True(t, info.IsAdmin)
}

func TestValidateToken_InvalidToken(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()

	_, err := auth.ValidateToken(ctx, q, "invalid-token")
	require.Error(t, err)
}

func TestContextUserRoundtrip(t *testing.T) {
	info := &auth.UserInfo{
		ID:       "user-1",
		OrgID:    "org-1",
		Username: "alice",
		IsAdmin:  true,
	}

	ctx := auth.WithUser(context.Background(), info)
	got := auth.GetUser(ctx)
	require.NotNil(t, got)
	assert.Equal(t, info.ID, got.ID)
}

func TestMustGetUser_NoUser(t *testing.T) {
	_, err := auth.MustGetUser(context.Background())
	require.Error(t, err)
}

func TestResolveOrgID_EmptyReturnsPersonalOrg(t *testing.T) {
	q := setupDB(t)
	orgID, userID := createTestUser(t, q)

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(context.Background(), q, user, "")
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_MemberReturnsOrgID(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, q)

	_ = q.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   1,
	})

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(ctx, q, user, orgID)
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_NonMemberReturnsNotFound(t *testing.T) {
	q := setupDB(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, q)

	otherOrgID := id.Generate()
	_ = q.CreateOrg(ctx, gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	_, err := auth.ResolveOrgID(ctx, q, user, otherOrgID)
	require.Error(t, err)

	connectErr, ok := err.(*connect.Error)
	require.True(t, ok, "expected *connect.Error")
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}
