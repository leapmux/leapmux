// Package testutil provides shared test helpers for hub packages.
package testutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

// CreateTestAdmin creates a default admin user ("admin"/"admin123") with a
// personal org and OWNER membership. This mirrors what the old bootstrap
// used to do in non-solo mode.
func CreateTestAdmin(t *testing.T, q *gendb.Queries) {
	t.Helper()
	ctx := context.Background()
	orgID := id.Generate()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "admin", IsPersonal: 1})
	require.NoError(t, err)
	hash, err := password.Hash("admin123")
	require.NoError(t, err)
	userID := id.Generate()
	err = q.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "admin",
		PasswordHash: hash,
		DisplayName:  "Admin",
		PasswordSet:  1,
		IsAdmin:      1,
	})
	require.NoError(t, err)
	err = q.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})
	require.NoError(t, err)
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
