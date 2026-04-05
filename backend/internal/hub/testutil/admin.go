// Package testutil provides shared test helpers for hub packages.
package testutil

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
)

// CreateTestAdmin bootstraps a default admin user ("admin"/"admin123") with
// a personal org and OWNER membership, using dev-mode bootstrap.
func CreateTestAdmin(t *testing.T, sqlDB *sql.DB, q *gendb.Queries) {
	t.Helper()
	require.NoError(t, bootstrap.Run(context.Background(), sqlDB, q, false, true))
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
