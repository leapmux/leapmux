// Package usernames defines the reserved-username constants and predicates
// shared across Hub packages (bootstrap, auth, service, cmd/leapmux). It
// sits below service/auth/bootstrap in the import graph so all of them can
// reference a single source of truth. Named in the plural to avoid shadowing
// common `username` variables in callers.
package usernames

import "strings"

// Solo is the reserved username for the single passwordless user auto-created
// and auto-authenticated in solo mode.
const Solo = "solo"

// Admin is the conventional username for the first administrator. Reserved in
// anonymous public signup but allowed in the /setup flow and admin-initiated
// creation paths.
const Admin = "admin"

func normalize(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// IsReservedSystem reports whether a username is reserved in every creation
// path (public signup, setup signup, OAuth signup, CLI user-create). Covers
// Solo: a user by that name in a non-solo database would be auto-authenticated
// for every request if the same data-dir were later opened in solo mode (see
// auth/interceptor.go).
func IsReservedSystem(u string) bool {
	return normalize(u) == Solo
}

// IsReservedPublic reports whether a username is reserved for anonymous,
// post-setup signup paths (public SignUp, public OAuth completion). Covers
// Admin. Setup-mode signup and the admin CLI accept the name because they do
// not call this predicate.
func IsReservedPublic(u string) bool {
	return normalize(u) == Admin
}

// IsReservedForPublicSignup reports whether a username is reserved in any
// anonymous signup context — both the system rule and the public rule apply.
// Use this in paths that have no setup-mode exemption (e.g. OAuth completion,
// post-setup SignUp). Equivalent to IsReservedSystem(u) || IsReservedPublic(u)
// but normalizes the input once.
func IsReservedForPublicSignup(u string) bool {
	n := normalize(u)
	return n == Solo || n == Admin
}
