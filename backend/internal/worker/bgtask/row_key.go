package bgtask

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The two reserved row-key prefixes. A stored key that carries one was BUILT
// by this package, never chosen by a provider: fresh provider input that
// starts with either is escaped before it is stored, so a native key cannot
// imitate a derived one and address another task's row.
const (
	// derivedRowKeyPrefix marks a row key this package derived because the
	// provider's own key was unusable (over RowKeyByteLimit or invalid UTF-8).
	derivedRowKeyPrefix = "leapmux-derived-key:"
	// escapedRowKeyPrefix marks a row key this package escaped because the
	// provider's own key was USABLE but starts with a reserved prefix.
	escapedRowKeyPrefix = "leapmux-escaped-key:"
)

// RowKey is one registry row key, typed at the only place it may be created:
// fresh provider input. Its canonical spelling is frozen at construction, so
// passing the typed value on can never re-derive it -- the double-normalization
// that used to orphan rows is unrepresentable rather than merely avoided.
//
// The zero RowKey is the empty key: a call that carries no registry linkage.
type RowKey struct {
	canonical string
	// reason states why the canonical key is not the provider's own bytes, for
	// the one log line a moved key leaves behind. It carries no copy of the raw
	// key, which can be arbitrary provider bytes of any shape.
	reason error
	// rawBytes is the raw key's length: the compact diagnostic that says how
	// large the provider's identity was without retaining it.
	rawBytes int
}

// ErrRowKeyEscapedPrefix is the reason a reserved-shaped native key carries.
var ErrRowKeyEscapedPrefix = errors.New("the native row key starts with a reserved prefix")

// NewRowKey types fresh provider input. It is TOTAL, like NormalizeRowKey:
// every input answers a storable key.
//
//   - A usable key without a reserved prefix is stored as its own bytes.
//   - A usable key that starts with a reserved prefix is ESCAPED: hashed under
//     escapedRowKeyPrefix, so it cannot address the row a genuinely derived or
//     escaped key identifies. Two providers' keys collide only through a
//     sha256 collision.
//   - An unusable key (over-long, invalid UTF-8) is DERIVED: hashed under
//     derivedRowKeyPrefix.
//
// It never trims and never truncates: both map two identities onto one string,
// and a row key is the second half of the (owner_agent_id, row_key) primary
// key that every later upsert, status change, close and rename addresses.
func NewRowKey(raw string) RowKey {
	reason := ValidateRowKey(raw)
	if reason == nil && !HasReservedRowKeyPrefix(raw) {
		return RowKey{canonical: raw, rawBytes: len(raw)}
	}
	key := RowKey{rawBytes: len(raw)}
	if reason == nil {
		reason = ErrRowKeyEscapedPrefix
		key.canonical = escapedRowKeyPrefix + hashRowKey(raw)
	} else {
		key.canonical = derivedRowKeyPrefix + hashRowKey(raw)
	}
	key.reason = reason
	return key
}

// String returns the canonical spelling, for the SQL and protobuf
// serialization sites that store or send the key.
func (k RowKey) String() string { return k.canonical }

// Reason reports why the canonical key is not the provider's own bytes, or nil
// when it is. The error names the rule; it retains no raw key bytes.
func (k RowKey) Reason() error { return k.reason }

// RawByteLength reports how many bytes the provider's own key carried.
func (k RowKey) RawByteLength() int { return k.rawBytes }

// Identity converts the typed key to its stored form.
func (k RowKey) Identity() RowIdentity { return RowIdentity{stored: k.canonical} }

// RowIdentity is a registry row key LOADED from durable storage or from a
// registry snapshot. It is opaque: nothing may re-interpret it as fresh
// provider input, because a stored key can carry a reserved prefix that fresh
// input would escape. Its construction is private to this package, which is
// what keeps the fresh-native and stored-identity directions apart.
type RowIdentity struct {
	stored string
}

// String returns the stored spelling, for the SQL and protobuf serialization
// sites that compare, store or send an identity that already exists.
func (r RowIdentity) String() string { return r.stored }

// ParseRowIdentity loads a stored key WITHOUT interpreting it as fresh native
// input: a derived or escaped key validates as itself, where NewRowKey would
// escape it and move the row.
//
// A stored key may be anything ValidateRowKey accepts, except a key that
// carries a reserved prefix but is not well-formed (prefix plus exactly 64
// lowercase hex digits). That spelling is unreachable through NewRowKey, so
// reading one means the stored bytes are corrupt.
func ParseRowIdentity(stored string) (RowIdentity, error) {
	if err := ValidateRowKey(stored); err != nil {
		return RowIdentity{}, err
	}
	for _, prefix := range []string{derivedRowKeyPrefix, escapedRowKeyPrefix} {
		if !strings.HasPrefix(stored, prefix) {
			continue
		}
		digest := strings.TrimPrefix(stored, prefix)
		if len(digest) != sha256.Size*2 {
			return RowIdentity{}, fmt.Errorf("the stored row key carries %q without a complete digest", prefix)
		}
		if !isLowercaseHex(digest) {
			return RowIdentity{}, fmt.Errorf("the stored row key digest under %q must be lowercase hex", prefix)
		}
	}
	return RowIdentity{stored: stored}, nil
}

// isLowercaseHex reports whether s is entirely lowercase hexadecimal digits,
// which is the one shape hex.EncodeToString produces.
func isLowercaseHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// HasReservedRowKeyPrefix reports whether a fresh provider key starts with one
// of the two prefixes this package reserves. Such a key is stored escaped, so
// it can never imitate a key this package built.
func HasReservedRowKeyPrefix(s string) bool {
	return strings.HasPrefix(s, derivedRowKeyPrefix) || strings.HasPrefix(s, escapedRowKeyPrefix)
}

func hashRowKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NormalizeRowKey returns the row key to store for the provider-supplied key s,
// which is NewRowKey(s).String(). See NewRowKey for the rule and its reasons;
// the string form exists for the call sites that pass a key through unchanged,
// and every one of them hands it FRESH provider bytes.
func NormalizeRowKey(s string) string {
	return NewRowKey(s).String()
}
