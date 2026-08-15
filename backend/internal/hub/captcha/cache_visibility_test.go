package captcha

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRefreshesAfterTTL proves the cache expiry path re-reads the
// row (the property the admin CLI's runtime changes depend on) by aging the
// cache out manually and mutating the row through a second connection.
func TestResolveRefreshesAfterTTL(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir()+"/hub.db", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	m := NewManager(st, ks, false)

	_, err = m.ChallengeJSON(ctx) // provisions + caches defaults
	require.NoError(t, err)

	// A second store (the admin CLI stand-in) flips the algorithm. The
	// update carries no secret — it must preserve the provisioned one.
	require.NoError(t, st.CaptchaConfig().Update(ctx, store.UpdateCaptchaConfigParams{
		Enabled:                true,
		Algorithm:              "SHA-256",
		Cost:                   1000,
		MemoryCost:             0,
		Parallelism:            0,
		ChallengeExpirySeconds: 1200,
	}))

	// Before TTL expiry the cached config is still served.
	res, err := m.resolve(ctx)
	require.NoError(t, err)
	assert.Equal(t, "PBKDF2/SHA-256", res.cfg.Algorithm)

	// Age the cache past the TTL and re-resolve.
	m.mu.Lock()
	m.cachedAt = time.Now().Add(-2 * cacheTTL)
	m.mu.Unlock()
	res, err = m.resolve(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SHA-256", res.cfg.Algorithm, "expired cache must re-read the row")
}
