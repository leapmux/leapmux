// Package pkce implements PKCE (Proof Key for Code Exchange) helpers
// shared by the OAuth/device-auth client (CLI) and the authorization
// server (hub). Both sides MUST agree on the S256 transform; keeping a
// single implementation removes the risk of drift.
package pkce

import (
	"crypto/sha256"
	"encoding/base64"
)

// S256 returns the base64-url-no-pad SHA-256 of verifier — the value
// that goes in `code_challenge` when `code_challenge_method=S256`.
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
