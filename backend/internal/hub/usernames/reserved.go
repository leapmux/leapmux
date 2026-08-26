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

// IsReservedForSignup reports whether a sign-up may not claim a username.
//
// setupMode says whether this sign-up creates the hub's FIRST account. Solo
// is refused in every mode: a user by that name in a non-solo database would
// be auto-authenticated for every request if the same data-dir were later
// opened in solo mode (see auth/interceptor.go). Admin is squat-protected in
// anonymous public signup and claimable by the first administrator, so the
// setup flows accept it.
//
// ONE predicate for every sign-up flavor, because they all ask this question
// and they must not answer it differently. Password sign-up spelled the two
// rules inline while the passkey and OAuth paths shared a public-only
// predicate, so the first administrator could claim `admin` with a password
// and not with a passkey.
func IsReservedForSignup(u string, setupMode bool) bool {
	n := normalize(u)
	return n == Solo || (!setupMode && n == Admin)
}
