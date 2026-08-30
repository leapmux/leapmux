package channelwire

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

func TestAllowRekey(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	t.Run("deny before contracts.MinRekeyInterval without soft nonce", func(t *testing.T) {
		assert.False(t, AllowRekey(base.Add(49*time.Minute), base, false))
	})
	t.Run("deny one nanosecond before contracts.MinRekeyInterval", func(t *testing.T) {
		assert.False(t, AllowRekey(base.Add(contracts.MinRekeyInterval-time.Nanosecond), base, false))
	})
	t.Run("allow at contracts.MinRekeyInterval", func(t *testing.T) {
		assert.True(t, AllowRekey(base.Add(contracts.MinRekeyInterval), base, false))
	})
	t.Run("allow before contracts.MinRekeyInterval when soft nonce", func(t *testing.T) {
		assert.True(t, AllowRekey(base.Add(time.Minute), base, true))
	})
	t.Run("deny zero lastRekeyAt without soft nonce", func(t *testing.T) {
		assert.False(t, AllowRekey(base, time.Time{}, false))
	})
	t.Run("allow zero lastRekeyAt when soft nonce", func(t *testing.T) {
		assert.True(t, AllowRekey(base, time.Time{}, true))
	})
}

func TestShouldInitiateRekey(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	t.Run("deny before contracts.SessionKeyMaxAge without soft nonce", func(t *testing.T) {
		assert.False(t, ShouldInitiateRekey(base.Add(59*time.Minute), base, false))
	})
	t.Run("deny one nanosecond before contracts.SessionKeyMaxAge", func(t *testing.T) {
		assert.False(t, ShouldInitiateRekey(base.Add(contracts.SessionKeyMaxAge-time.Nanosecond), base, false))
	})
	t.Run("allow at contracts.SessionKeyMaxAge without soft nonce", func(t *testing.T) {
		assert.True(t, ShouldInitiateRekey(base.Add(contracts.SessionKeyMaxAge), base, false))
	})
	t.Run("allow early when soft nonce", func(t *testing.T) {
		assert.True(t, ShouldInitiateRekey(base.Add(time.Minute), base, true))
	})
	t.Run("deny brand-new channel without soft nonce", func(t *testing.T) {
		assert.False(t, ShouldInitiateRekey(base, time.Time{}, false))
	})
	t.Run("allow brand-new channel when soft nonce", func(t *testing.T) {
		assert.True(t, ShouldInitiateRekey(base, time.Time{}, true))
	})
}

func TestRejectRetryAfter(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	t.Run("remaining time until contracts.MinRekeyInterval", func(t *testing.T) {
		assert.Equal(t, 10*time.Minute, RejectRetryAfter(base.Add(40*time.Minute), base))
	})
	t.Run("zero once contracts.MinRekeyInterval has elapsed", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), RejectRetryAfter(base.Add(contracts.MinRekeyInterval), base))
	})
	t.Run("contracts.MinRekeyInterval when lastRekeyAt is zero", func(t *testing.T) {
		assert.Equal(t, contracts.MinRekeyInterval, RejectRetryAfter(base, time.Time{}))
	})
}

func TestPastHardCeiling(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	t.Run("deny under hard ceiling", func(t *testing.T) {
		assert.False(t, PastHardCeiling(base.Add(contracts.SessionKeyMaxAge+9*time.Minute), base))
	})
	t.Run("allow at hard ceiling", func(t *testing.T) {
		assert.True(t, PastHardCeiling(base.Add(contracts.SessionKeyHardCeiling), base))
	})
	t.Run("deny zero lastRekeyAt", func(t *testing.T) {
		assert.False(t, PastHardCeiling(base, time.Time{}))
	})
}
