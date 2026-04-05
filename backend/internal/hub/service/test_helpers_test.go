package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	return req
}

// sessionFromCookie extracts the session ID from a Set-Cookie header value.
func sessionFromCookie(t *testing.T, setCookie string) string {
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

func testConfig() *config.Config {
	return &config.Config{
		APITimeoutSeconds:            config.DefaultAPITimeoutSeconds,
		AgentStartupTimeoutSeconds:   config.DefaultAgentStartupTimeoutSeconds,
		WorktreeCreateTimeoutSeconds: config.DefaultWorktreeCreateTimeoutSeconds,
	}
}

func testConfigWithSignup() *config.Config {
	cfg := testConfig()
	cfg.SignupEnabled = true
	return cfg
}

// createTestAdmin creates a default admin user ("admin"/"admin123") in the
// test database, mirroring what the old bootstrap used to do in non-solo mode.
func createTestAdmin(t *testing.T, sqlDB *sql.DB, q *gendb.Queries) {
	t.Helper()
	ctx := context.Background()
	orgID := id.Generate()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{
		ID:         orgID,
		Name:       "admin",
		IsPersonal: 1,
	})
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
		Email:        "",
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
