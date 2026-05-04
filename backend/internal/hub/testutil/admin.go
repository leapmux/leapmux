// Package testutil provides shared test helpers for hub packages.
package testutil

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// OpenTestStore opens an in-memory SQLite store with migrations applied.
// (sqlite.Open runs migrations automatically.)
func OpenTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestAdminUsername and TestAdminPassword are the credentials created by
// CreateTestAdmin. Exported so service and e2e tests that log in as the
// fixture don't hardcode the strings in multiple places.
const (
	TestAdminUsername = usernames.Admin
	TestAdminPassword = "admin123"
)

// Argon2id is intentionally slow. Hash the fixture password once per process
// so tests that seed the admin user don't each pay ~200ms.
var (
	testAdminHashOnce sync.Once
	testAdminHash     string
	testAdminHashErr  error
)

func cachedTestAdminHash() (string, error) {
	testAdminHashOnce.Do(func() {
		testAdminHash, testAdminHashErr = password.Hash(TestAdminPassword)
	})
	return testAdminHash, testAdminHashErr
}

// CreateTestAdmin creates the default admin fixture directly via the store,
// bypassing the SignUp RPC (and therefore its reserved-username check).
func CreateTestAdmin(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	hash, err := cachedTestAdminHash()
	require.NoError(t, err)

	orgID := id.Generate()
	userID := id.Generate()

	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       TestAdminUsername,
			IsPersonal: true,
		}); err != nil {
			return err
		}
		if err := tx.Users().Create(ctx, store.CreateUserParams{
			ID:           userID,
			OrgID:        orgID,
			Username:     TestAdminUsername,
			PasswordHash: hash,
			DisplayName:  "Admin",
			Email:        "",
			PasswordSet:  true,
			IsAdmin:      true,
		}); err != nil {
			return err
		}
		return tx.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
			OrgID:  orgID,
			UserID: userID,
			Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		})
	}))
}

// CreateTestUser creates a non-admin user with the given credentials.
// Mirrors CreateTestAdmin but with IsAdmin=false and the supplied
// password instead of the cached fixture. Useful for cross-user tests.
func CreateTestUser(t *testing.T, st store.Store, username, plainPassword string) string {
	t.Helper()
	ctx := context.Background()

	hash, err := password.Hash(plainPassword)
	require.NoError(t, err)

	orgID := id.Generate()
	userID := id.Generate()

	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       username,
			IsPersonal: true,
		}); err != nil {
			return err
		}
		if err := tx.Users().Create(ctx, store.CreateUserParams{
			ID:           userID,
			OrgID:        orgID,
			Username:     username,
			PasswordHash: hash,
			DisplayName:  username,
			PasswordSet:  true,
		}); err != nil {
			return err
		}
		return tx.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
			OrgID:  orgID,
			UserID: userID,
			Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		})
	}))
	return userID
}

// SessionFromCookie extracts the session ID from a Set-Cookie header value.
func SessionFromCookie(t *testing.T, setCookie string) string {
	t.Helper()
	require.NotEmpty(t, setCookie, "Set-Cookie header must be present")
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, auth.CookieName+"=") {
			return strings.TrimPrefix(part, auth.CookieName+"=")
		}
	}
	t.Fatalf("session cookie %q not found in Set-Cookie: %s", auth.CookieName, setCookie)
	return ""
}
