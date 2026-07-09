package sqlite

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSessionTestStore(t *testing.T) (*sqliteStore, *sql.DB) {
	t.Helper()
	opened, err := Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	st := opened.(*sqliteStore)
	return st, st.conn.shared.db
}

func waitForSQLiteFraction(t *testing.T, db *sql.DB, match func(float64) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var seconds float64
		require.NoError(t, db.QueryRow(`SELECT CAST(strftime('%f', 'now') AS REAL)`).Scan(&seconds))
		if match(math.Mod(seconds, 1)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for SQLite clock fraction")
}

func seedFractionalSession(t *testing.T, st store.Store, expiresAt time.Time) (string, string) {
	t.Helper()
	orgID := storetest.SeedOrg(t, st, "fractional-session-org", true)
	user := storetest.SeedUser(t, st, orgID, "fractional-session-user")
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: expiresAt,
	}))
	return sessionID, user.ID
}

func TestSessionExpiryPredicatesPreserveFractionalPrecision(t *testing.T) {
	t.Run("future session remains active within its expiry second", func(t *testing.T) {
		st, db := newSessionTestStore(t)
		sessionID, userID := seedFractionalSession(t, st, time.Now().Add(time.Hour))
		waitForSQLiteFraction(t, db, func(fraction float64) bool { return fraction < 0.3 })
		_, err := db.Exec(`UPDATE user_sessions SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+0.500 seconds') WHERE id = ?`, sessionID)
		require.NoError(t, err)

		_, err = st.Sessions().GetByID(context.Background(), sessionID)
		require.NoError(t, err)
		_, err = st.Sessions().ValidateWithUser(context.Background(), sessionID)
		require.NoError(t, err)
		byUser, err := st.Sessions().ListByUserID(context.Background(), userID)
		require.NoError(t, err)
		assert.Len(t, byUser, 1)
		all, err := st.Sessions().ListAllActive(context.Background(), store.ListAllActiveSessionsParams{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, all, 1)
	})

	t.Run("expired session is deleted within its expiry second", func(t *testing.T) {
		st, db := newSessionTestStore(t)
		sessionID, _ := seedFractionalSession(t, st, time.Now().Add(time.Hour))
		waitForSQLiteFraction(t, db, func(fraction float64) bool { return fraction > 0.7 })
		_, err := db.Exec(`UPDATE user_sessions SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-0.500 seconds') WHERE id = ?`, sessionID)
		require.NoError(t, err)

		deleted, err := st.Cleanup().HardDeleteExpiredSessions(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)
	})
}

func TestListAllActiveSessionsPreservesFractionalCursorPrecision(t *testing.T) {
	st, db := newSessionTestStore(t)
	sessionID, _ := seedFractionalSession(t, st, time.Now().Add(time.Hour))
	base := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	lastActive := base.Add(400 * time.Millisecond)
	_, err := db.Exec(`UPDATE user_sessions SET last_active_at = ? WHERE id = ?`, lastActive, sessionID)
	require.NoError(t, err)

	rows, err := st.Sessions().ListAllActive(context.Background(), store.ListAllActiveSessionsParams{
		Cursor: base.Add(600 * time.Millisecond).Format(time.RFC3339Nano),
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sessionID, rows[0].ID)
}
