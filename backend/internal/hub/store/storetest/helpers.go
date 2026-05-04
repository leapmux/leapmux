package storetest

import (
	"context"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/require"
)

// RequireNotFound asserts that err is store.ErrNotFound.
func RequireNotFound(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, store.ErrNotFound)
}

// RequireConflict asserts that err is store.ErrConflict.
func RequireConflict(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, store.ErrConflict)
}

var ctx = context.Background()

// SeedOrg creates an org and returns its ID.
func SeedOrg(t *testing.T, st store.Store, name string, isPersonal bool) string {
	t.Helper()
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{
		ID:         orgID,
		Name:       name,
		IsPersonal: isPersonal,
	})
	require.NoError(t, err)
	return orgID
}

// SeedUser creates a user in the given org and returns the fetched user.
func SeedUser(t *testing.T, st store.Store, orgID, username string) *store.User {
	t.Helper()
	userID := id.Generate()
	err := st.Users().Create(ctx, store.CreateUserParams{
		ID:            userID,
		OrgID:         orgID,
		Username:      username,
		PasswordHash:  "hash-" + username,
		DisplayName:   "Display " + username,
		Email:         username + "@example.com",
		EmailVerified: true,
		PasswordSet:   true,
		IsAdmin:       false,
	})
	require.NoError(t, err)

	user, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	return user
}

// SeedOrgMember creates an org membership.
func SeedOrgMember(t *testing.T, st store.Store, orgID, userID string, role leapmuxv1.OrgMemberRole) {
	t.Helper()
	err := st.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   role,
	})
	require.NoError(t, err)
}

// SeedRegistrationKey creates a worker_registration_keys row owned by
// createdBy with the given expires_at and returns its id.
func SeedRegistrationKey(t *testing.T, st store.Store, createdBy string, expiresAt time.Time) string {
	t.Helper()
	regID := id.Generate()
	err := st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID:        regID,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
	})
	require.NoError(t, err)
	return regID
}

// SeedWorker creates a worker registered by the given user and returns the fetched worker.
func SeedWorker(t *testing.T, st store.Store, registeredBy string) *store.Worker {
	t.Helper()
	workerID := id.Generate()
	token := id.Generate()
	err := st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       token,
		RegisteredBy:    registeredBy,
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
	})
	require.NoError(t, err)

	worker, err := st.Workers().GetByID(ctx, workerID)
	require.NoError(t, err)
	return worker
}

// SeedOAuthProvider creates an OAuth provider and returns the fetched provider.
func SeedOAuthProvider(t *testing.T, st store.Store, name string) *store.OAuthProvider {
	t.Helper()
	provID := id.Generate()
	err := st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID:           provID,
		ProviderType: "oidc",
		Name:         name,
		IssuerURL:    "https://issuer.example.com",
		ClientID:     "client-" + name,
		ClientSecret: []byte("secret-" + name),
		Scopes:       "openid profile email",
		TrustEmail:   true,
		Enabled:      true,
	})
	require.NoError(t, err)

	prov, err := st.OAuthProviders().GetByID(ctx, provID)
	require.NoError(t, err)
	return prov
}

// SeedWorkspace creates a workspace and returns its ID.
func SeedWorkspace(t *testing.T, st store.Store, orgID, ownerID, title string) string {
	t.Helper()
	wsID := id.Generate()
	err := st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          wsID,
		OrgID:       orgID,
		OwnerUserID: ownerID,
		Title:       title,
	})
	require.NoError(t, err)
	return wsID
}

// SeedSession creates a session with 24h expiry and returns the fetched session.
func SeedSession(t *testing.T, st store.Store, userID string) *store.UserSession {
	t.Helper()
	sessID := id.Generate()
	err := st.Sessions().Create(ctx, store.CreateSessionParams{
		ID:        sessID,
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		UserAgent: "test-agent",
		IPAddress: "127.0.0.1",
	})
	require.NoError(t, err)

	sess, err := st.Sessions().GetByID(ctx, sessID)
	require.NoError(t, err)
	return sess
}
