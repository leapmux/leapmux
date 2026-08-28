// Package pkce implements PKCE (Proof Key for Code Exchange) helpers
// shared by the OAuth/device-auth client (CLI) and the authorization
// server (hub). Both sides MUST agree on the S256 transform; keeping a
// single implementation removes the risk of drift.
package pkce

import (
	"crypto/sha256"
	"encoding/base64"
)

// pkceMinLen and pkceMaxLen are the RFC 7636 section 4.1 limits: a
// code_verifier (and therefore a `plain` challenge) is 43-128 characters of
// the unreserved set. The FLOOR is a security limit, not a style rule: a
// 2-character verifier has a preimage an attacker finds in a handful of
// guesses, which defeats PKCE for exactly the clients it protects.
const (
	pkceMinLen = 43
	pkceMaxLen = 128
)

// S256 returns the base64-url-no-pad SHA-256 of verifier — the value
// that goes in `code_challenge` when `code_challenge_method=S256`.
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidVerifier reports whether s is a code_verifier RFC 7636 section 4.1
// admits: 43-128 characters from the unreserved set (ALPHA / DIGIT / "-" / "."
// / "_" / "~"). Both the authorize stage and the token stage must apply the
// SAME limit, so it lives here beside the transform they already share.
func ValidVerifier(s string) bool {
	return validCodeChallengeInput(s)
}

// ValidChallenge reports whether s is a code_challenge RFC 7636 section 4.2
// admits. An S256 challenge is 43 characters of base64url, which the same
// limit covers.
func ValidChallenge(s string) bool {
	return validCodeChallengeInput(s)
}

func validCodeChallengeInput(s string) bool {
	if len(s) < pkceMinLen || len(s) > pkceMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == '_' || c == '~':
		default:
			return false
		}
	}
	return true
}
