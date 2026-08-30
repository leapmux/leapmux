// Package usernames defines the reserved-username constants and predicates
// shared across Hub packages (bootstrap, auth, service, cmd/leapmux). It
// sits below service/auth/bootstrap in the import graph so all of them can
// reference a single source of truth. Named in the plural to avoid shadowing
// common `username` variables in callers.
//
// The names and the two sets are owned by contracts/validate.json and arrive
// through the generated contracts package; the browser's
// SYSTEM_RESERVED_USERNAMES / PUBLIC_RESERVED_USERNAMES are generated from
// the same file.
package usernames

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Solo is the reserved username for the single passwordless user auto-created
// and auto-authenticated in solo mode.
const Solo = contracts.UsernameSolo

// Admin is the conventional username for the first administrator. Reserved in
// anonymous public signup but allowed in the /setup flow and admin-initiated
// creation paths.
const Admin = contracts.UsernameAdmin

func normalize(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// IsReservedSystem reports whether a username is reserved in every creation
// path (public signup, setup signup, OAuth signup, CLI user-create). Covers
// Solo: the hub would auto-authenticate a user by that name in a non-solo
// database for every request, if an operator later opened the same data-dir in
// solo mode (see auth/interceptor.go).
func IsReservedSystem(u string) bool {
	return contracts.UsernamesSystemReserved[normalize(u)]
}

// IsReservedForSignup reports whether a sign-up may not claim a username.
//
// setupMode says whether this sign-up creates the hub's FIRST account. The hub
// refuses Solo in every mode: it would auto-authenticate a user by that name in
// a non-solo database for every request, if an operator later opened the same
// data-dir in solo mode (see auth/interceptor.go). Admin is squat-protected in
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
	return contracts.UsernamesSystemReserved[n] || (!setupMode && contracts.UsernamesPublicReserved[n])
}
