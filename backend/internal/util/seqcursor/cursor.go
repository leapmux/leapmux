// Package seqcursor signs and verifies versioned 41-byte sequence position
// cursors bound to a caller-supplied domain and scope via length-prefixed
// HMAC-SHA256.
package seqcursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math"
)

const (
	// Version is the cursor wire format version byte.
	Version = 1
	// PositionSize is the 1-byte version prefix plus the 8-byte big-endian sequence.
	PositionSize = 1 + 8
	// Size is the total length of an encoded cursor (position + HMAC-SHA256).
	Size = PositionSize + sha256.Size
)

// Signer signs and verifies sequence positions for one fixed key, domain, and
// scope, reusing its pre-encoded scope prefix and HMAC state across calls.
// A Signer is not safe for concurrent use across goroutines.
type Signer struct {
	digest hash.Hash
	prefix []byte
}

// NewSigner constructs a Signer bound to key, domain, and scope fields.
func NewSigner(key []byte, domain string, scope ...string) Signer {
	prefixLen := 8 + len(domain) + 8
	for _, field := range scope {
		prefixLen += 8 + len(field)
	}
	prefix := make([]byte, 0, prefixLen)
	prefix = appendLengthPrefixed(prefix, domain)
	for _, field := range scope {
		prefix = appendLengthPrefixed(prefix, field)
	}
	prefix = binary.BigEndian.AppendUint64(prefix, PositionSize)
	return Signer{
		digest: hmac.New(sha256.New, key),
		prefix: prefix,
	}
}

// Sign encodes sequence and appends its HMAC-SHA256 tag.
func (s *Signer) Sign(sequence int64) []byte {
	token := make([]byte, PositionSize, Size)
	token[0] = Version
	binary.BigEndian.PutUint64(token[1:], uint64(sequence))
	s.digest.Reset()
	_, _ = s.digest.Write(s.prefix)
	_, _ = s.digest.Write(token)
	return s.digest.Sum(token)
}

// Verify decodes cursor and verifies its HMAC against this signer's scope.
func (s *Signer) Verify(cursor []byte) (int64, bool) {
	sequence, ok := DecodePosition(cursor)
	if !ok || !hmac.Equal(cursor, s.Sign(sequence)) {
		return 0, false
	}
	return sequence, true
}

// DecodePosition extracts the non-negative sequence position from a version-1
// cursor without verifying its MAC.
func DecodePosition(cursor []byte) (int64, bool) {
	if len(cursor) != Size || cursor[0] != Version {
		return 0, false
	}
	raw := binary.BigEndian.Uint64(cursor[1:PositionSize])
	if raw > math.MaxInt64 {
		return 0, false
	}
	return int64(raw), true
}

// Digest computes a length-prefixed HMAC-SHA256 tag over purpose and fields.
func Digest(key []byte, purpose string, fields ...string) []byte {
	mac := hmac.New(sha256.New, key)
	writeLengthPrefixed(mac, purpose)
	for _, field := range fields {
		writeLengthPrefixed(mac, field)
	}
	return mac.Sum(nil)
}

func appendLengthPrefixed(dst []byte, field string) []byte {
	dst = binary.BigEndian.AppendUint64(dst, uint64(len(field)))
	return append(dst, field...)
}

func writeLengthPrefixed(mac hash.Hash, field string) {
	var lenBytes [8]byte
	binary.BigEndian.PutUint64(lenBytes[:], uint64(len(field)))
	_, _ = mac.Write(lenBytes[:])
	_, _ = mac.Write([]byte(field))
}
