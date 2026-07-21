package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateRegistrationKeyStoresExpiresAtCanonical pins the canonical-strftime
// storage contract for the registration-key write path, mirroring
// TestCreateUserSessionStoresExpiresAtCanonical. ListRegistrationKeysAdmin
// compares expires_at raw against strftime('%Y-%m-%dT%H:%M:%fZ', now), so a
// revert of the Create wrap to a direct time.Time bind would silently
// mis-compare the active/expired filter (and the liveness guards in
// Extend/Consume). The result-level storetest writes expires_at through the
// store but does not assert the on-disk layout, so the contract needs a direct
// storage read.
func TestCreateRegistrationKeyStoresExpiresAtCanonical(t *testing.T) {
	st, db := newSessionTestStore(t)
	orgID := storetest.SeedOrg(t, st, "canonical-regkey-org")
	user := storetest.SeedUser(t, st, orgID, "canonical-regkey-user")
	expiresAt := time.Date(2026, 7, 20, 12, 34, 56, 789_456_789, time.UTC)
	keyID := id.Generate()
	require.NoError(t, st.RegistrationKeys().Create(context.Background(), store.CreateRegistrationKeyParams{
		ID:        keyID,
		CreatedBy: user.ID,
		ExpiresAt: expiresAt,
	}))

	// CAST strips the DATETIME decltype so modernc returns the stored bytes
	// verbatim instead of trimming trailing fractional zeros on scan.
	var stored string
	require.NoError(t, db.QueryRow(`SELECT CAST(expires_at AS TEXT) FROM worker_registration_keys WHERE id = ?`, keyID).Scan(&stored))
	assert.Equal(t, timefmt.Format(expiresAt), stored,
		"expires_at must be stored in the canonical strftime layout the raw-string filters assume; got %q", stored)
}
