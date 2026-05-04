package auth

import "strings"

// BearerPrefix is the canonical prefix for HTTP Authorization headers
// carrying a bearer credential.
const BearerPrefix = "Bearer "

// BearerToken extracts a bearer credential from an Authorization header.
// Returns the token and true on a well-formed "Bearer <token>" header,
// or "" and false otherwise. Trims surrounding whitespace from the
// token so trailing CR/LF or stray spaces don't leak into downstream
// equality checks.
func BearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, BearerPrefix) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(BearerPrefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}
