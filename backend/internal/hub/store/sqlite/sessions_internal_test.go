package sqlite

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"testing"
	"time"

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
	orgID := storetest.SeedOrg(t, st, "fractional-session-org")
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
		byUserPage, err := st.Sessions().ListByUserID(context.Background(), store.ListUserSessionsParams{UserID: userID, PageParams: store.PageParams{Limit: 1000}})
		require.NoError(t, err)
		byUser := byUserPage.Rows
		assert.Len(t, byUser, 1)
		page, err := st.Sessions().ListAllActive(context.Background(), store.ListAllActiveSessionsParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		assert.Len(t, page.Rows, 1)
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
		require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
			ID:        sessID,
			UserID:    userID,
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
	})
	require.NoError(t, err)
	// The cursor row itself is excluded (id not < itself); the tied row with
	// the smaller id survives via the equality branch; the 400ms row survives
	// via the strict < branch. DESC order puts the tied row first.
	require.Len(t, page.Rows, 2)
	assert.Equal(t, tieBelowID, page.Rows[0].ID)
	assert.Equal(t, beforeID, page.Rows[1].ID)
}

// TestCreateUserSessionStoresExpiresAtCanonical pins the storage contract the
// raw-string expiry/cursor filters rely on: CreateUserSession wraps the bound
// ExpiresAt in strftime('%Y-%m-%dT%H:%M:%fZ'), so the column holds the canonical
// layout. A revert to binding time.Time directly (the driver's layout) would
// make expires_at > strftime(...,'now') silently mis-compare while the
// result-level tests still passed (they write expires_at via db.Exec), so the
// contract needs a direct storage assertion. The sub-millisecond digits also
// prove the wrap quantizes to the canonical millisecond layout.
func TestCreateUserSessionStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	orgID := storetest.SeedOrg(t, st, "canonical-expiry-org")
	user := storetest.SeedUser(t, st, orgID, "canonical-expiry-user")
	expiresAt := time.Date(2026, 7, 20, 12, 34, 56, 789_456_789, time.UTC)
	sessionID := id.Generate()
	require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
		ID:        sessionID,
		UserID:    user.ID,
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
	// expires_at is the caller-supplied instant, wrapped canonical by CreateUserSession.
	assert.Equal(t, timefmt.Format(expiresAt), expiresAtStored,
		"expires_at must be stored in the canonical strftime layout the raw-string filters assume; got %q", expiresAtStored)
	// created_at + last_active_at are written by the column DEFAULT
	// (strftime('%Y-%m-%dT%H:%M:%fZ','now')), NOT by an explicit Go-bound wrap,
	// so a migration change to the DEFAULT is the one path the explicit-wrap
	// tests cannot catch. Pin their layout: a canonical value parses cleanly
	// under sqliteTimeFormat; a non-canonical one (e.g. modernc's driver layout
	// with a space at byte 10) does not.
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
// regression shipped green. This test touches with a same-day sub-millisecond
// expiry and asserts both the on-disk layout AND that GetByID still finds the
// row, so neither the storage drift nor its user-visible consequence can
// silently return.
func TestTouchStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	orgID := storetest.SeedOrg(t, st, "touch-canonical-org")
	user := storetest.SeedUser(t, st, orgID, "touch-canonical-user")
	sessionID := storetest.SeedSession(t, st, user.ID).ID

	// Pin last_active_at to a known-old canonical instant so Touch's
	// `last_active_at < ?` guard provably matches and the UPDATE fires.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.Exec(`UPDATE user_sessions SET last_active_at = ? WHERE id = ?`, timefmt.Format(old), sessionID)
	require.NoError(t, err)

	// Same UTC day as "now", with sub-millisecond precision -- the exact shape
	// that exposed the raw-bind bug.
	newExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	n, err := st.Sessions().Touch(context.Background(), store.TouchSessionParams{
		ID:           sessionID,
		ExpiresAt:    newExpiry,
		LastActiveAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "Touch must fire against the backdated row")

	// Direct contract: the raw-string liveness filter GetUserSessionByID uses
	// must see the touched row as live. Reading the column back through Scan
	// is not a reliable witness here -- modernc reformats a DATETIME value on
	// scan, so it hides a non-canonical on-disk layout. The filter result and
	// GetByID below exercise the exact SQL path that broke.
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

	// The user-visible consequence: GetByID uses that same raw-string filter,
	// so a non-canonical stored value returns ErrNotFound on the request after
	// each Touch.
	_, err = st.Sessions().GetByID(context.Background(), sessionID)
	require.NoError(t, err, "touched session must remain visible to GetByID")
}

// TestKeysetCursorTrailingZeroMillisecondTie pins that the SQL-side strftime
// write paths store canonical fixed 3-digit fractional seconds ON DISK, so the
// keyset predicate's "=" tiebreak branch byte-matches a timefmt.Format-decoded
// cursor even when the boundary millisecond ends in a trailing zero. (modernc
// trims trailing zeros only when a DATETIME column is scanned into a Go string
// -- a driver presentation artifact; production reads scan time.Time and the
// comparisons run SQL-side on the stored bytes.) A prior review round misread
// that scan artifact as on-disk variability and documented a same-ms tied-row
// drop here; this test is the deterministic counterexample, and it guards
// against a future driver regression that would make the drop real.
func TestKeysetCursorTrailingZeroMillisecondTie(t *testing.T) {
	st, db := newSessionTestStore(t)
	orgID := storetest.SeedOrg(t, st, "tie-org")
	user := storetest.SeedUser(t, st, orgID, "tie-user")

	mk := func() string {
		sessionID := id.Generate()
		require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
			ID:        sessionID,
			UserID:    user.ID,
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
		UserID:     user.ID,
		PageParams: store.PageParams{Cursor: store.EncodeCursor(tieInstant, boundary), Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2, "the tied smaller-id row and the earlier row must both survive the boundary")
	assert.Equal(t, expectTied, page.Rows[0].ID)
	assert.Equal(t, below, page.Rows[1].ID)
	assert.False(t, page.HasMore())
}
