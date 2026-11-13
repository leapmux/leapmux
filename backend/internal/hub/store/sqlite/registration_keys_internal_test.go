package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateRegistrationKeyStoresExpiresAtCanonical checks the canonical
// storage format for registration keys, as the session test does.
// Create binds the expiry through SQLiteTime, which floors it to
// milliseconds and formats it in Go. The active-list filter compares the
// stored value against a bound SQLiteNullTime; an invalid value disables
// that filter. Extend and Consume also bind their current time through
// SQLiteTime. A raw time.Time bind would use the driver's format and could
// break those raw-string comparisons. Read the stored bytes directly to
// check the format independently of the result-level tests.
func TestCreateRegistrationKeyStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	user := storetest.SeedUser(t, st, "canonical-regkey-user")
	expiresAt := time.Date(2026, 7, 20, 12, 34, 56, 789_456_789, time.UTC)
	keyID := id.Generate()
	require.NoError(t, st.RegistrationKeys().Create(context.Background(), store.CreateRegistrationKeyParams{KeyHash: storetest.FixtureSecretHash(id.Generate()),
		ID:        keyID,
		CreatedBy: userid.MustNew(user.ID),
		ExpiresAt: expiresAt,
	}))

	// CAST strips the DATETIME decltype so modernc returns the stored bytes
	// verbatim instead of trimming trailing fractional zeros on scan.
	var stored string
	require.NoError(t, db.QueryRow(`SELECT CAST(expires_at AS TEXT) FROM worker_registration_keys WHERE id = ?`, keyID).Scan(&stored))
	assert.Equal(t, timefmt.Format(expiresAt), stored,
		"expires_at must be stored in the canonical strftime layout the raw-string filters assume; got %q", stored)
}
