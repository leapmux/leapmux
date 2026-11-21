// Package sessioncookie exposes the generated HTTP cookie names that
// authenticate a LeapMux Hub session.
package sessioncookie

import "github.com/leapmux/leapmux/generated/contracts"

const (
	// Name is the session cookie used without TLS.
	Name = contracts.SessionCookieName
	// SecureName is the __Host-prefixed session cookie used with TLS.
	SecureName = contracts.SecureSessionCookieName
)

// IsName reports whether name is one of the two Hub session cookie spellings.
func IsName(name string) bool {
	return name == Name || name == SecureName
}
