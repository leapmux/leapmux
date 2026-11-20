// Package sessioncookie owns the two HTTP cookie names that authenticate a
// LeapMux Hub session.
package sessioncookie

const (
	// Name is the session cookie used without TLS.
	Name = "leapmux-session"
	// SecureName is the __Host-prefixed session cookie used with TLS.
	SecureName = "__Host-leapmux-session"
)

// IsName reports whether name is one of the two Hub session cookie spellings.
func IsName(name string) bool {
	return name == Name || name == SecureName
}
