package captcha

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRefreshesAfterTTL proves the snapshot expiry path re-reads
// the row (the property the admin CLI's runtime changes depend on) by
// freezing the settings clock, mutating the row through a second manager
// (the admin CLI stand-in), and aging the first manager's snapshot out.
func TestResolveRefreshesAfterTTL(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir()+"/hub.db", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const ttl = time.Second
	set := settings.NewManager(st, ks, SettingsDescriptors(),
		settings.WithTTL(ttl), settings.WithNow(func() time.Time { return now }))
	require.NoError(t, set.Load(ctx))
	m := NewManager(st, set, false)

	_, err = m.AltchaChallengeJSON(ctx) // provisions + caches defaults
	require.NoError(t, err)
	provisioned := AltchaKey.Of(set.Snapshot(ctx))

	// A second manager (the admin CLI stand-in) swaps the settings. The
	// settings-only update never touches the provisioned secret.
	admin := settings.NewManager(st, ks, SettingsDescriptors())
	require.NoError(t, admin.Load(ctx))
	require.NoError(t, admin.Update(ctx, AltchaKey, json.RawMessage(cheapAltchaSettings)))

	// Before TTL expiry the cached config is still served.
	res, err := m.resolve(ctx)
	require.NoError(t, err)
	assert.Equal(t, "PBKDF2/SHA-256", res.cfg.Altcha.Algorithm)

	// Age the snapshot past the TTL and re-resolve.
	now = now.Add(2 * ttl)
	res, err = m.resolve(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SHA-256", res.cfg.Altcha.Algorithm, "expired snapshot must re-read the row")

	// The settings-only update preserved the provisioned secret.
	preserved := AltchaKey.Of(admin.Snapshot(ctx))
	assert.Equal(t, provisioned.HMACKey, preserved.HMACKey)
}
