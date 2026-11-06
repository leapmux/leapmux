package service

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChallengeExpiryReplayAndCapacity drives the challenge store
// across its whole rule set: expiry under a moved clock, the single use
// of a consumed challenge, the replacement of an attempt's outstanding
// challenge, and the capacity bound.
func TestChallengeExpiryReplayAndCapacity(t *testing.T) {
	clock := quartz.NewMock(t)
	store := newRegistrationChallengeStore(clock)

	id1, nonce1, expiresAt, err := store.Issue("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	assert.Len(t, id1, 32, "the challenge identity is 128 bits of hex")
	assert.Len(t, nonce1, 32, "the nonce is 32 bytes")
	assert.True(t, expiresAt.After(clock.Now()), "the challenge expires in the future")

	t.Run("a consumed challenge cannot serve twice", func(t *testing.T) {
		got, ok := store.Consume(id1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		require.True(t, ok)
		assert.Equal(t, nonce1, got)

		_, ok = store.Consume(id1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		assert.False(t, ok, "the second use of a consumed challenge refuses")
	})

	t.Run("a challenge refuses a foreign attempt", func(t *testing.T) {
		id2, _, _, err := store.Issue("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		require.NoError(t, err)
		_, ok := store.Consume(id2, "cccccccccccccccccccccccccccccccc")
		assert.False(t, ok, "a challenge belongs to exactly one attempt")
		// The refused consumption did not burn the challenge for its own
		// attempt.
		_, ok = store.Consume(id2, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		assert.True(t, ok)
	})

	t.Run("an expired challenge refuses", func(t *testing.T) {
		id3, _, _, err := store.Issue("dddddddddddddddddddddddddddddddd")
		require.NoError(t, err)
		// Drive the clock past the TTL; the mock fires nothing on its
		// own, so advancing past the deadline is enough.
		clock.Advance(registrationChallengeTTL + time.Second).MustWait(t.Context())
		_, ok := store.Consume(id3, "dddddddddddddddddddddddddddddddd")
		assert.False(t, ok, "an expired challenge refuses")
	})

	t.Run("a new challenge replaces the attempt's outstanding one", func(t *testing.T) {
		first, _, _, err := store.Issue("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
		require.NoError(t, err)
		second, _, _, err := store.Issue("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
		require.NoError(t, err)
		assert.NotEqual(t, first, second)
		_, ok := store.Consume(first, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
		assert.False(t, ok, "the replaced challenge is gone")
		_, ok = store.Consume(second, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
		assert.True(t, ok)
	})

	t.Run("the capacity bound refuses new challenges", func(t *testing.T) {
		full := newRegistrationChallengeStore(clock)
		for i := 0; i < maxOutstandingRegistrationChallenges; i++ {
			var attempt [16]byte
			_, err := rand.Read(attempt[:])
			require.NoError(t, err)
			_, _, _, err = full.Issue(string(hexOf(attempt[:])))
			require.NoError(t, err)
		}
		_, _, _, err := full.Issue("ffffffffffffffffffffffffffffffff")
		assert.ErrorIs(t, err, errChallengeCapacity)
	})
}

// hexOf spells bytes as lowercase hex.
func hexOf(b []byte) []byte {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return out
}

// The attempt identity validation admits exactly the 32 lowercase
// hexadecimal characters the worker mints.
func TestValidateAttemptIdentity(t *testing.T) {
	assert.True(t, validateAttemptIdentity("a9a0425886d148f68a8f94716ed47a78"))
	assert.False(t, validateAttemptIdentity(""), "empty is not an identity")
	assert.False(t, validateAttemptIdentity("A9A0425886D148F68A8F94716ED47A78"), "uppercase is refused")
	assert.False(t, validateAttemptIdentity("a9a0425886d148f68a8f94716ed47a7"), "31 characters are refused")
	assert.False(t, validateAttemptIdentity("a9a0425886d148f68a8f94716ed47a788"), "33 characters are refused")
	assert.False(t, validateAttemptIdentity("g9a0425886d148f68a8f94716ed47a78"), "non-hexadecimal characters are refused")
}

// The recovery message holds the documented byte sequence and nothing
// else: prefix, both decoded identities, the nonce, and the digest.
func TestRecoverySignedMessageShape(t *testing.T) {
	attempt := "a9a0425886d148f68a8f94716ed47a78"
	challenge := "b9a0425886d148f68a8f94716ed47a78"
	nonce := make([]byte, 32)
	digest := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i)
		digest[i] = byte(255 - i)
	}
	message := recoverySignedMessage(attempt, challenge, nonce, digest)
	assert.Equal(t, len(recoveryContextPrefix)+16+16+32+32, len(message))
	assert.Equal(t, recoveryContextPrefix, string(message[:len(recoveryContextPrefix)]))
	assert.Equal(t, nonce, message[len(recoveryContextPrefix)+32:len(recoveryContextPrefix)+32+32])
	assert.Equal(t, digest, message[len(message)-32:])
}
