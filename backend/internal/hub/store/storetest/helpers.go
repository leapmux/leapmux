package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

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

// ListAllSessions returns every live session for userID via the paginated
// ListByUserID, using one oversized page: for tests that assert on the full
// membership rather than paging behavior.
func ListAllSessions(t *testing.T, st store.Store, userID string) []store.UserSession {
	t.Helper()
	page, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{UserID: userid.MustNew(userID), PageParams: store.PageParams{Limit: 1000}})
	require.NoError(t, err)
	return page.Rows
}

// pageThroughByOne pages through a keyset-paginated listing one row at a
// time using the store-produced next cursor, and returns the row ids in
// visit order. Used by the same-millisecond tie tests: paging with limit=1
// forces every row onto a page boundary, so a broken tiebreaker shows up as
// a skipped or duplicated id. The loop follows HasMore, which the store
// derives from a limit+1 probe row -- so it also asserts the final page
// reports HasMore=false instead of handing out a dead extra cursor.
//
// The cap is a safety net for a paging bug that re-returns rows forever, NOT
// the expected termination path: a correct keyset walk ends via the empty-page
// or !HasMore break. It sits well above any real test's row count (currently
// <=3), and the loop fails loudly if it ever reaches the cap -- otherwise a
// runaway pager would exit silently and only the caller's ElementsMatch (which
// a future author could weaken or omit) would catch the missing rows.
func pageThroughByOne[T store.PageCursorer](t *testing.T, fetch func(cursor string) (store.Page[T], error)) []string {
	t.Helper()
	var seen []string
	cursor := ""
	const safetyCap = 100
	for i := 0; i < safetyCap; i++ {
		page, err := fetch(cursor)
		require.NoError(t, err)
		if len(page.Rows) == 0 {
			require.False(t, page.HasMore(), "empty page must not report more rows")
			break
		}
		require.Len(t, page.Rows, 1)
		_, id := page.Rows[0].PageCursor()
		seen = append(seen, id)
		if !page.HasMore() {
			require.Empty(t, page.NextCursor, "terminal page must not carry a cursor")
			break
		}
		require.NotEmpty(t, page.NextCursor)
		cursor = page.NextCursor
		if i == safetyCap-1 {
			require.Failf(t, "paging did not terminate", "walked %d pages without reaching a terminal page (cursor re-returning rows forever?)", safetyCap)
		}
	}
	return seen
}

// SeedOrg creates an org and returns its ID.
func SeedOrg(t *testing.T, st store.Store, name string) string {
	t.Helper()
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{
		ID:   orgID,
		Name: name,
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

// SeedRegistrationKey creates a worker_registration_keys row owned by
// createdBy with the given expires_at and returns its id.
func SeedRegistrationKey(t *testing.T, st store.Store, createdBy string, expiresAt time.Time) string {
	t.Helper()
	regID := id.Generate()
	err := st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID:        regID,
		CreatedBy: userid.MustNew(createdBy),
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
		RegisteredBy:    userid.MustNew(registeredBy),
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
		OwnerUserID: userid.MustNew(ownerID),
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
		UserID:    userid.MustNew(userID),
		ExpiresAt: time.Now().Add(24 * time.Hour),
		UserAgent: "test-agent",
		IPAddress: "127.0.0.1",
	})
	require.NoError(t, err)

	sess, err := st.Sessions().GetByID(ctx, sessID)
	require.NoError(t, err)
	return sess
}
