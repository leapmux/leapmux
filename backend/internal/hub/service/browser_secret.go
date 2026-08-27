package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// The hash for every secret the hub hands to ONE browser and then checks on
// the way back.
//
// Three flows use it -- the emailed password-reset token, the OAuth
// flow-binding nonce, and the pending-signup nonce that carries that binding
// across the hand-off -- so it belongs to none of them. It lived in
// auth_passkey.go, which is the one flow that does NOT use it.

// hashBrowserSecret hashes a secret the hub hands to a browser and then
// stores only in hashed form: the emailed password-reset token, the OAuth
// flow-binding nonce, and the pending-signup nonce that carries that binding
// across the hand-off. A read of the row that holds the hash must not
// reconstruct a value that completes the flow, so every such secret goes
// through this one function rather than through a per-caller copy of the
// same two lines.
//
// Every call site calls THIS function. Two one-line renames wrapped it for
// a while, which made one concept read as three and stretched the third
// name past what it described.
func hashBrowserSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// browserSecretMatches reports whether the secret a browser presented is the
// one the hub minted a stored hash from.
//
// It carries BOTH halves of the rule, and the first is the one worth having
// in a single place: an EMPTY stored hash never matches. A row minted
// without a browser cookie cannot be completed at all, so failing closed is
// what makes the binding a property of every row rather than of the rows
// somebody remembered to check. If each call site writes that guard out, a
// single `||` loses it on one path and keeps it on the other.
//
// The comparison is constant time. The values are hex digests rather than
// secrets, so the timing tells an attacker little -- but the two call sites
// are the OAuth callback and the pending-signup hand-off, and neither is a
// place to reason about how little.
func browserSecretMatches(storedHash, presented string) bool {
	if storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashBrowserSecret(presented))) == 1
}
