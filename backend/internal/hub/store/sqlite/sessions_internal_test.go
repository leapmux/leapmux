package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/timefmt"
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

func seedFractionalSession(t *testing.T, st store.Store, expiresAt time.Time) (string, string) {
	t.Helper()
	user := storetest.SeedUser(t, st, "fractional-session-user")
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{SecretHash: storetest.FixtureSecretHash("ssssssssssssssssssssssssssssssssssssssssssssssss"),
		ID:        sessionID,
		UserID:    userid.MustNew(user.ID),
		ExpiresAt: expiresAt,
	}))
	return sessionID, user.ID
}

func TestSessionExpiryPredicatesPreserveFractionalPrecision(t *testing.T) {
	ctx := context.Background()
	expiry := time.Date(2025, 1, 2, 3, 4, 5, 700_000_000, time.UTC)
	st, db := newSessionTestStore(t)
	sessionID, userID := seedFractionalSession(t, st, expiry)
	_, err := db.ExecContext(ctx, `UPDATE user_sessions SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', ?) WHERE id = ?`, timefmt.Format(expiry), sessionID)
	require.NoError(t, err)
	for _, offset := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			now := expiry.Add(offset)
			row, err := st.Sessions().GetByID(ctx, sessionID, now)
			joined, joinedErr := st.Sessions().ValidateWithUser(ctx, sessionID, now)
			byUser, listErr := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{UserID: userid.MustNew(userID), PageParams: store.PageParams{Limit: 10}}, now)
			require.NoError(t, listErr)
			all, allErr := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{PageParams: store.PageParams{Limit: 10}}, now)
			require.NoError(t, allErr)
			if offset < 0 {
				require.NoError(t, err)
				require.NoError(t, joinedErr)
				assert.Equal(t, sessionID, row.ID)
				assert.Equal(t, userID, joined.UserID)
				require.Len(t, byUser.Rows, 1)
				require.Len(t, all.Rows, 1)
				assert.Equal(t, sessionID, byUser.Rows[0].ID)
				assert.Equal(t, sessionID, all.Rows[0].ID)
			} else {
				require.ErrorIs(t, err, store.ErrNotFound)
				require.ErrorIs(t, joinedErr, store.ErrNotFound)
				assert.Empty(t, byUser.Rows)
				assert.Empty(t, all.Rows)
			}
		})
	}
	cleanupStore, _ := newSessionTestStore(t)
	seedFractionalSession(t, cleanupStore, expiry)
	for _, tc := range []struct {
		offset  time.Duration
		deleted int64
	}{
		{-time.Millisecond, 0}, {0, 0}, {time.Millisecond, 1},
	} {
		deleted, err := cleanupStore.Cleanup().HardDeleteExpiredSessions(ctx, expiry.Add(tc.offset))
		require.NoError(t, err)
		assert.Equal(t, tc.deleted, deleted, "cleanup at %s", tc.offset)
	}
}

// TestListAllActiveSessionsPreservesFractionalCursorPrecision pins the
// invariant the keyset predicate relies on: last_active_at is stored in the
// fixed strftime('%Y-%m-%dT%H:%M:%fZ') layout (the column DEFAULT and Touch
// both write it SQL-side) and decodeCursorParams formats the cursor identically, so
// the raw string comparison is byte-exact -- sub-second distinctions survive
// (400ms < 600ms), and a row TIED with the cursor instant is included or
// excluded purely by the (= AND id <) tiebreak. If the stored layout and the
// cursor layout ever diverge, the equality branch stops matching and the tied
// row below the cursor id silently disappears, failing this test.
func TestListAllActiveSessionsPreservesFractionalCursorPrecision(t *testing.T) {
	st, db := newSessionTestStore(t)
	beforeID, userID := seedFractionalSession(t, st, time.Now().Add(time.Hour))
	// Two more sessions for the same user, pinned exactly at the cursor
	// instant, with hand-picked ids on both sides of the cursor id.
	const tieBelowID, tieCursorID = "tie-a", "tie-b"
	for _, sessID := range []string{tieBelowID, tieCursorID} {
		require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{SecretHash: storetest.FixtureSecretHash("ssssssssssssssssssssssssssssssssssssssssssssssss"),
			ID:        sessID,
			UserID:    userid.MustNew(userID),
			ExpiresAt: time.Now().Add(time.Hour),
		}))
	}

	base := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	cursorTime := base.Add(600 * time.Millisecond)
	// Backdate all three rows in the production strftime layout (fixed three
	// fractional digits), exactly as the column DEFAULT and Touch would.
	for sessID, lastActive := range map[string]time.Time{
		beforeID:    base.Add(400 * time.Millisecond),
		tieBelowID:  cursorTime,
		tieCursorID: cursorTime,
	} {
		_, err := db.Exec(`UPDATE user_sessions SET last_active_at = ? WHERE id = ?`, timefmt.Format(lastActive), sessID)
		require.NoError(t, err)
	}

	page, err := st.Sessions().ListAllActive(context.Background(), store.ListAllActiveSessionsParams{
		PageParams: store.PageParams{Cursor: store.EncodeCursor(cursorTime, tieCursorID), Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	// The cursor row itself is excluded (id not < itself); the tied row with
	// the smaller id survives via the equality branch; the 400ms row survives
	// via the strict < branch. DESC order puts the tied row first.
	require.Len(t, page.Rows, 2)
	assert.Equal(t, tieBelowID, page.Rows[0].ID)
	assert.Equal(t, beforeID, page.Rows[1].ID)
}

// TestCreateUserSessionStoresExpiresAtCanonical checks the storage format
// required by the raw-string expiry filters. Create binds ExpiresAt through
// sqltime.SQLiteTime, which floors it to milliseconds and formats it in Go.
// The expiry filters compare the stored value against a bound SQLiteTime
// in the same format. Binding a raw time.Time would use the driver's format
// and could misclassify a session that expires on the same UTC day.
// The direct storage assertion checks the format independently of the
// result-level expiry tests. The sub-millisecond input also checks that
// the stored value discards precision below one millisecond.
func TestCreateUserSessionStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	user := storetest.SeedUser(t, st, "canonical-expiry-user")
	expiresAt := time.Date(2026, 7, 20, 12, 34, 56, 789_456_789, time.UTC)
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{SecretHash: storetest.FixtureSecretHash("ssssssssssssssssssssssssssssssssssssssssssssssss"),
		ID:        sessionID,
		UserID:    userid.MustNew(user.ID),
		ExpiresAt: expiresAt,
	}))

	// CAST strips the DATETIME decltype so modernc returns expires_at's stored
	// bytes verbatim for the exact-equality pin below (a bare scan trims
	// trailing fractional zeros). created_at/last_active_at stay bare on
	// purpose: their shape-only assertions tolerate the trim, and the comment
	// below documents the scanned-digit variability they must absorb.
	var expiresAtStored, createdAtStored, lastActiveAtStored string
	require.NoError(t, db.QueryRow(
		`SELECT CAST(expires_at AS TEXT), created_at, last_active_at FROM user_sessions WHERE id = ?`,
		sessionID,
	).Scan(&expiresAtStored, &createdAtStored, &lastActiveAtStored))
	// SQLiteTime stores the caller-supplied instant in the canonical format.
	assert.Equal(t, timefmt.Format(expiresAt), expiresAtStored,
		"expires_at must be stored in the canonical strftime layout the raw-string filters assume; got %q", expiresAtStored)
	// The column defaults write created_at and last_active_at with SQL
	// strftime. These writes bypass SQLiteTime, so the expires_at assertion
	// cannot detect a change to their defaults. Check the separator and UTC
	// suffix that the raw-string cursor comparisons require.
	for _, col := range []struct{ name, val string }{
		{"created_at", createdAtStored},
		{"last_active_at", lastActiveAtStored},
	} {
		// The column DEFAULT writes strftime('%Y-%m-%dT%H:%M:%fZ','now'), which
		// stores fixed 3-digit fractional seconds ON DISK -- but scanning a
		// DATETIME column into a Go string goes through modernc's presentation
		// layer, which trims trailing fractional zeros (a stored ".130Z" arrives
		// as ".13Z"), so the scanned digit count varies here and a strict ".000"
		// parse would be over-tight. What IS load-bearing is the canonical
		// *shape* -- 'T' at byte 10 and a 'Z' suffix -- because modernc's driver
		// layout ("YYYY-MM-DD HH:MM:SS+00:00", space at byte 10) sorts BEFORE
		// the canonical 'T'-prefixed RHS and silently breaks every raw-string
		// liveness/cursor filter. Pin the shape so a future write path that
		// binds a raw time.Time fails loudly here.
		require.GreaterOrEqual(t, len(col.val), 20, "%s too short to be canonical; got %q", col.name, col.val)
		assert.Equal(t, "T", string(col.val[10]), "%s byte 10 must be 'T' (canonical), not a space (driver layout); got %q", col.name, col.val)
		assert.True(t, strings.HasSuffix(col.val, "Z"), "%s must end in 'Z' (canonical UTC), not an offset; got %q", col.name, col.val)
	}
}

// TestTouchStoresExpiresAtCanonical pins the SAME storage contract as
// TestCreateUserSessionStoresExpiresAtCanonical, but for the Touch write path.
// Touch is the only other session write path, and it is an inline query rather
// than a generated one -- a prior version bound `expires_at = ?` raw, modernc
// serialized it as "2026-07-20 12:34:56.123+00:00" (space + offset), and every
// raw-string liveness filter (GetByID, ValidateWithUser, ListAllActive,
// ListByUserID) then deemed the touched session expired -- logging the user
// out on the request following each Touch. The result-level touch tests used a
// 48h expiry whose different UTC day masked the byte-10 mismatch, so the
// regression shipped green. This test touches with an expiry one hour ahead at
// millisecond precision and asserts both the on-disk layout and that GetByID still finds the
// row, so neither the storage drift nor its user-visible consequence can
// silently return.
func TestTouchStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	user := storetest.SeedUser(t, st, "touch-canonical-user")
	sessionID := storetest.SeedSession(t, st, user.ID).ID

	// Pin last_active_at to a known-old canonical instant so Touch's
	// `last_active_at < ?` guard provably matches and the UPDATE fires.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Exec(`UPDATE user_sessions SET last_active_at = ? WHERE id = ?`, timefmt.Format(old), sessionID)
	require.NoError(t, err)

	// Set the expiry one hour ahead at millisecond precision.
	newExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	n, err := st.Sessions().Touch(context.Background(), store.TouchSessionParams{
		ID:           sessionID,
		ExpiresAt:    newExpiry,
		LastActiveAt: time.Now().UTC(),
	}, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "Touch must fire against the backdated row")

	// Probe the stored expiry with a canonical timestamp from SQLite's clock.
	// This checks raw-string comparison independently of the Go bind.
	// GetByID below checks the production predicate, which binds the caller's
	// clock as a SQLiteTime. A bare DATETIME scan can hide the stored format
	// because the driver reformats it.
	var live bool
	require.NoError(t, db.QueryRow(
		`SELECT expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM user_sessions WHERE id = ?`,
		sessionID,
	).Scan(&live))
	assert.True(t, live,
		"touched session's expires_at must compare live under the raw-string "+
			"strftime filter (the path that returned false pre-fix and logged users out)")

	// Direct layout pin: Touch binds expires_at as a sqltime.SQLiteTime, so the
	// on-disk value must equal timefmt.Format(newExpiry) exactly. A future
	// Touch refactor that binds expires_at as a raw time.Time would store
	// modernc's driver layout here and this assertion fails before the liveness
	// filter regresses.
	// CAST strips the column's DATETIME decltype so modernc returns the stored
	// bytes verbatim -- a bare scan trims trailing fractional zeros (".720Z"
	// arrives as ".72Z") and fails this pin on every ms-ends-in-zero instant.
	var expiresAtStored string
	require.NoError(t, db.QueryRow(`SELECT CAST(expires_at AS TEXT) FROM user_sessions WHERE id = ?`, sessionID).Scan(&expiresAtStored))
	assert.Equal(t, timefmt.Format(newExpiry), expiresAtStored,
		"Touch must store expires_at in the canonical strftime layout; got %q", expiresAtStored)

	// GetByID must also find the touched session when its expiry predicate
	// compares against the caller's bound clock.
	_, err = st.Sessions().GetByID(context.Background(), sessionID, time.Now().UTC())
	require.NoError(t, err, "touched session must remain visible to GetByID")
}

// TestKeysetCursorTrailingZeroMillisecondTie pins that the SQL-side strftime
// write paths store canonical fixed 3-digit fractional seconds ON DISK, so the
// keyset predicate's "=" tiebreak branch byte-matches a cursor bound through
// SQLiteNullTime even when the boundary millisecond ends in a trailing zero. (modernc
// trims trailing zeros only when a DATETIME column is scanned into a Go string
// -- a driver presentation artifact; production reads scan time.Time and the
// comparisons run SQL-side on the stored bytes.) A prior review round misread
// that scan artifact as on-disk variability and documented a same-ms tied-row
// drop here; this test is the deterministic counterexample, and it guards
// against a future driver regression that would make the drop real.
func TestKeysetCursorTrailingZeroMillisecondTie(t *testing.T) {
	st, db := newSessionTestStore(t)
	user := storetest.SeedUser(t, st, "tie-user")

	mk := func() string {
		sessionID := id.Generate()
		require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{SecretHash: storetest.FixtureSecretHash("ssssssssssssssssssssssssssssssssssssssssssssssss"),
			ID:        sessionID,
			UserID:    userid.MustNew(user.ID),
			ExpiresAt: time.Now().Add(time.Hour),
		}))
		return sessionID
	}
	below, tie1, tie2 := mk(), mk(), mk()

	// Rewrite last_active_at through SQLite's own strftime -- the same
	// formatting layer the production write paths use -- at a millisecond whose
	// final digit is zero (.130), with two rows tied at the boundary instant.
	tieInstant := time.Date(2026, 1, 2, 3, 4, 5, 130_000_000, time.UTC)
	set := func(sessionID string, at time.Time) {
		_, err := db.Exec(`UPDATE user_sessions SET last_active_at = strftime('%Y-%m-%dT%H:%M:%fZ', ?) WHERE id = ?`,
			timefmt.Format(at), sessionID)
		require.NoError(t, err)
	}
	set(below, tieInstant.Add(-time.Millisecond))
	set(tie1, tieInstant)
	set(tie2, tieInstant)

	// The on-disk invariant the byte-exact compare rests on: strftime kept all
	// three fractional digits, trailing zero included. length() runs SQL-side
	// on the stored bytes, before any driver string conversion.
	var short int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE length(last_active_at) != 24`).Scan(&short))
	require.Zero(t, short, "on-disk strftime values must keep fixed 3-digit fractional seconds")

	// Page boundary ON the tie: cursor at (tieInstant, larger tie id). The "="
	// tiebreak branch must admit the smaller-id tied row; the "<" branch the
	// earlier row. Under the misread bug neither branch would have matched and
	// both rows would silently vanish from the next page.
	boundary, expectTied := tie1, tie2
	if boundary < expectTied {
		boundary, expectTied = expectTied, boundary
	}
	page, err := st.Sessions().ListByUserID(context.Background(), store.ListUserSessionsParams{
		UserID:     userid.MustNew(user.ID),
		PageParams: store.PageParams{Cursor: store.EncodeCursor(tieInstant, boundary), Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, page.Rows, 2, "the tied smaller-id row and the earlier row must both survive the boundary")
	assert.Equal(t, expectTied, page.Rows[0].ID)
	assert.Equal(t, below, page.Rows[1].ID)
	assert.False(t, page.HasMore())
}

func TestSessionUserForeignKeyRejectsMissingOwners(t *testing.T) {
	ctx := context.Background()
	st, db := newSessionTestStore(t)
	sessionID, ownerID := seedFractionalSession(t, st, time.Now().Add(time.Hour))
	for _, missing := range []string{"", id.Generate()} {
		_, err := db.ExecContext(ctx, `UPDATE user_sessions SET user_id = ? WHERE id = ?`, missing, sessionID)
		require.Error(t, err)
		require.Contains(t, strings.ToLower(err.Error()), "foreign key")
		var retained string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT user_id FROM user_sessions WHERE id = ?`, sessionID).Scan(&retained))
		assert.Equal(t, ownerID, retained)
	}
	other := storetest.SeedUser(t, st, "other-session-owner")
	_, err := db.ExecContext(ctx, `UPDATE user_sessions SET user_id = ? WHERE id = ?`, other.ID, sessionID)
	require.NoError(t, err)
	var updated string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT user_id FROM user_sessions WHERE id = ?`, sessionID).Scan(&updated))
	assert.Equal(t, other.ID, updated)
}
