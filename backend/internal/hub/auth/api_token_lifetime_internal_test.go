package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The absolute ceiling is a property of the CREDENTIAL, checked at every
// validation, and not of the leg that happens to compute a window.
//
// It used to be arithmetic at the mint and the rotation alone, so it held only
// for a row whose expiry AccessWindowFor or RefreshWindowFor wrote. A mint
// path that writes its own expiry -- AdminUserService.IssueAPIToken takes a
// ttl_seconds of up to a year -- could put a row past the ceiling that nothing
// afterwards re-read.
func TestAPITokenExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }

	t.Run("a live expiry inside the ceiling is live", func(t *testing.T) {
		t.Parallel()
		assert.False(t, apiTokenExpired(at(time.Hour), now.Add(-24*time.Hour), now))
	})

	t.Run("its own expiry still ends it", func(t *testing.T) {
		t.Parallel()
		assert.True(t, apiTokenExpired(at(-time.Second), now.Add(-24*time.Hour), now))
	})

	// The case the arithmetic missed: a row whose own expiry is still ahead,
	// on a credential authorized longer ago than the ceiling allows.
	t.Run("the ceiling ends it although its own expiry has not", func(t *testing.T) {
		t.Parallel()
		created := now.Add(-AbsoluteTokenLifetime - time.Hour)
		assert.True(t, apiTokenExpired(at(30*24*time.Hour), created, now),
			"a credential cannot outlive the consent that authorized it")
	})

	t.Run("a credential exactly at the ceiling is dead", func(t *testing.T) {
		t.Parallel()
		created := now.Add(-AbsoluteTokenLifetime)
		assert.True(t, apiTokenExpired(nil, created, now),
			"IsExpired treats the recorded instant as invalid, not as the last live one")
	})

	t.Run("a nil expiry is bound by the ceiling alone", func(t *testing.T) {
		t.Parallel()
		assert.False(t, apiTokenExpired(nil, now.Add(-time.Hour), now))
		assert.True(t, apiTokenExpired(nil, now.Add(-AbsoluteTokenLifetime-time.Hour), now))
	})

	// A zero created_at means the caller loaded no creation instant. Failing
	// closed there would refuse every live credential at once on a mapping
	// slip, which is a worse answer than skipping one of two deadlines.
	t.Run("a zero creation instant skips the ceiling", func(t *testing.T) {
		t.Parallel()
		assert.False(t, apiTokenExpired(at(time.Hour), time.Time{}, now))
		assert.True(t, apiTokenExpired(at(-time.Hour), time.Time{}, now),
			"the row's own expiry still applies")
	})
}
