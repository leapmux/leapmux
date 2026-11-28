package seqcursor

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignerRoundTripAndScopeBinding(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	s1 := NewSigner(key, "test domain", "owner-1", "journal-1", "scope-a")
	s2 := NewSigner(key, "test domain", "owner-1", "journal-1", "scope-b")
	sOtherKey := NewSigner(bytes.Repeat([]byte{0x43}, 32), "test domain", "owner-1", "journal-1", "scope-a")

	for _, seq := range []int64{0, 1, 42, math.MaxInt64} {
		tok := s1.Sign(seq)
		require.Len(t, tok, Size)

		decoded, ok := DecodePosition(tok)
		assert.True(t, ok)
		assert.Equal(t, seq, decoded)

		verified, ok := s1.Verify(tok)
		assert.True(t, ok)
		assert.Equal(t, seq, verified)

		_, ok = s2.Verify(tok)
		assert.False(t, ok, "different scope must not verify")
		_, ok = sOtherKey.Verify(tok)
		assert.False(t, ok, "different key must not verify")

		tampered := bytes.Clone(tok)
		tampered[len(tampered)-1] ^= 0x01
		_, ok = s1.Verify(tampered)
		assert.False(t, ok, "tampered MAC must not verify")
	}

	// Length-prefixing prevents boundary ambiguity ("ab"+"c" != "a"+"bc").
	sa := NewSigner(key, "domain", "ab", "c")
	sb := NewSigner(key, "domain", "a", "bc")
	assert.NotEqual(t, sa.Sign(7), sb.Sign(7))
}

func TestDecodePositionRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x11}, 32)
	s := NewSigner(key, "domain", "scope")
	valid := s.Sign(5)

	_, ok := DecodePosition(nil)
	assert.False(t, ok)
	_, ok = DecodePosition(valid[:Size-1])
	assert.False(t, ok)

	wrongVersion := bytes.Clone(valid)
	wrongVersion[0] = 2
	_, ok = DecodePosition(wrongVersion)
	assert.False(t, ok)

	overflow := bytes.Clone(valid)
	binary.BigEndian.PutUint64(overflow[1:PositionSize], uint64(math.MaxInt64)+1)
	_, ok = DecodePosition(overflow)
	assert.False(t, ok)
}

func TestDigestDeterministicAndScopeBound(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x99}, 32)
	d1 := Digest(key, "purpose", "a", "bc")
	d2 := Digest(key, "purpose", "ab", "c")
	assert.Len(t, d1, 32)
	assert.NotEqual(t, d1, d2)
	assert.Equal(t, d1, Digest(key, "purpose", "a", "bc"))
}
