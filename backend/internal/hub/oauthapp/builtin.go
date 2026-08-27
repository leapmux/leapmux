// Package oauthapp holds the identity of the apps LeapMux ships with.
//
// The values are constants of the BUILD, not configuration, so the migration
// seeds their rows and this package is what every other package reads them
// from. A store test asserts the seeded rows match these constants on all
// three dialects, so a hand-edited migration cannot quietly retire a built-in
// app.
package oauthapp

// ControlCLIClientID is the built-in control CLI: `leapmux control auth login`.
//
// It is a PUBLIC client -- a program on the user's own machine cannot keep a
// secret -- and its one registered redirect URI is the RFC 8252 section 7.3
// loopback form with no port, so the CLI's ephemeral listener matches on
// scheme, host and path with the port free.
const ControlCLIClientID = "leapmux-control-cli"

// ServiceAccountClientID is the app an administrator's out-of-band credential
// belongs to: `leapmux control admin api-token issue`.
//
// It runs NO flow. It has no redirect URI and no grant type, so nothing can
// drive an authorization through it; it exists so api_tokens.client_id can
// stay NOT NULL. "An administrator issued this out of band" is an ANSWER to
// "which app holds this credential", not a missing value -- a NULL would make
// every listing, join, cascade and consent path branch to express a fact the
// model carries perfectly well.
const ServiceAccountClientID = "leapmux-service-account"

// ControlCLIRedirectURI is the loopback redirect the control CLI registers.

// ControlCLIScopes is the ceiling the control CLI's seeded registration
// carries. It is a constant of the build for the same reason the redirect
// address is: a credential minted for the built-in CLI authenticates against
// exactly this set, and a migration that edits one dialect's copy without the
// others drifts silently -- the store test pins the seeded rows to these
// constants so it cannot.
const ControlCLIScopes = "account:read account:write workspace:read workspace:write " +
	"worker:read worker:admin agent:read agent:write terminal:read terminal:write " +
	"file:read git:read git:write tunnel:open admin:read admin:users admin:settings admin:workers"

// ControlCLIGrantTypes is the flow set the CLI's seeded registration runs.
// See ControlCLIScopes for why it is a constant.
const ControlCLIGrantTypes = "authorization_code refresh_token urn:ietf:params:oauth:grant-type:device_code"

// ServiceAccountScopes is the ceiling the administrator-issued credential's
// seeded registration carries: the whole grantable vocabulary, so an
// out-of-band mint is limited by the issuing administrator alone. See
// ControlCLIScopes for why it is a constant.
const ServiceAccountScopes = ControlCLIScopes

// The port is absent on purpose; see MatchRedirectURI.
const ControlCLIRedirectURI = "http://127.0.0.1/callback"

// ControlCLIName and ServiceAccountName are what the consent screen and the
// credential list show.
const (
	ControlCLIName     = "LeapMux control CLI"
	ServiceAccountName = "Administrator-issued credential"
)

// IsBuiltIn reports whether a client id is one this build ships with. The two
// built-in registrations cannot be deleted or edited, because their fields are
// constants of the build rather than rows an administrator owns.
func IsBuiltIn(clientID string) bool {
	return clientID == ControlCLIClientID || clientID == ServiceAccountClientID
}

// BuiltInClientIDs lists them in a stable order, for a test or a listing that
// must cover both.
func BuiltInClientIDs() []string {
	return []string{ControlCLIClientID, ServiceAccountClientID}
}
