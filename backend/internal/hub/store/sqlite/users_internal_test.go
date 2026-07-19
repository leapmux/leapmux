package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

// TestSetPendingEmailStoresCanonicalFormat pins the storage contract of the
// third mixed-format-hazard column this package canonicalizes:
// users.pending_email_expires_at. SetPendingEmail wraps its bound instant in
// strftime (like CreateUserSession/Touch do for expires_at) because
// ConsumeVerificationAttempt's lockout branch writes the SAME column via
// strftime('now') -- a prior version bound the SetPendingEmail value raw, so
// the column held a mix of modernc's driver layout (space at byte 10) and the
// canonical layout ('T' at byte 10), and ClearStalePendingEmails' raw-string
// cutoff compare silently misjudged rows whose stored value and cutoff shared
// a UTC calendar day.
func TestSetPendingEmailStoresCanonicalFormat(t *testing.T) {
	st, db := newSessionTestStore(t)
	ctx := context.Background()
	orgID := storetest.SeedOrg(t, st, "pending-canonical-org")
	user := storetest.SeedUser(t, st, orgID, "pending-canonical-user")

	// Millisecond-aligned, same UTC day as "now" (matching the session
	// canonical-layout pins): strftime ROUNDS sub-millisecond digits while
	// formatSQLiteTime truncates, so a ms-aligned instant isolates the layout
	// assertion from that sub-ms rounding difference.
	expiry := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Millisecond)
	require.NoError(t, st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    user.ID,
		PendingEmail:          "new@example.com",
		PendingEmailToken:     "tok-1",
		PendingEmailExpiresAt: &expiry,
	}))

	// Direct layout pin: the on-disk value must equal formatSQLiteTime(expiry)
	// byte-for-byte. Comparing SQL-side (raw = CAST) avoids modernc's
	// scan-time reformatting, which would hide a non-canonical layout.
	var canonical bool
	require.NoError(t, db.QueryRow(
		`SELECT pending_email_expires_at = CAST(? AS TEXT) FROM users WHERE id = ?`,
		formatSQLiteTime(expiry), user.ID,
	).Scan(&canonical))
	assert.True(t, canonical,
		"SetPendingEmail must store the canonical strftime layout, not the raw driver layout")
}

// TestClearStalePendingEmailsSweepsLockoutRowSameDay reproduces the exact
// pre-fix failure: a pending_email_expires_at written by the
// ConsumeVerificationAttempt LOCKOUT branch (SQL-side strftime('now'),
// canonical 'T' at byte 10) compared against a cutoff bound from Go. The old
// code bound the cutoff raw (driver layout, ' ' at byte 10): on any run where
// the stored value and the cutoff shared a UTC calendar day, ' ' < 'T' made
// `stored < cutoff` false and the sweep silently skipped the row until the
// dates diverged. The fixed code binds formatSQLiteTime(cutoff), so a cutoff
// one minute in the future must clear the just-locked-out row.
func TestClearStalePendingEmailsSweepsLockoutRowSameDay(t *testing.T) {
	st, _ := newSessionTestStore(t)
	ctx := context.Background()
	orgID := storetest.SeedOrg(t, st, "lockout-sweep-org")
	user := storetest.SeedUser(t, st, orgID, "lockout-sweep-user")

	expiry := time.Now().UTC().Add(24 * time.Hour)
	require.NoError(t, st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    user.ID,
		PendingEmail:          "locked@example.com",
		PendingEmailToken:     "tok-lock",
		PendingEmailExpiresAt: &expiry,
	}))

	// Exceed the attempt budget so the lockout branch rewrites
	// pending_email_expires_at to strftime('now').
	for range 6 {
		_, err := st.Users().ConsumeVerificationAttempt(ctx, user.ID)
		require.NoError(t, err)
	}

	// A cutoff barely in the future, on the same UTC day as the lockout
	// write. Pre-fix this cleared 0 rows; it must clear exactly this one.
	n, err := st.Cleanup().ClearStalePendingEmails(ctx, time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n,
		"the same-day sweep must clear the lockout-expired pending email")
}
