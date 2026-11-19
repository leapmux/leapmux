// Package oauthapp holds the identity of the apps LeapMux ships with.
//
// The values are constants of the BUILD, not configuration, and this package
// is what every other package reads them from. Every store open seeds and
// reconciles their rows from here (store.SeedBuiltIns), rewriting only these
// constant columns and leaving the operator's decisions -- the step-up flag
// above all -- untouched across restarts. A store test asserts the seeded
// rows match these constants on all three dialects, so a hand-edited row
// cannot quietly retire or reshape a built-in app.
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

// DesktopGrantTypes is the flow set the desktop's seeded registration runs.
// See ControlCLIScopes for why it is a constant.
const DesktopGrantTypes = GrantTypeAuthorizationCode

// DesktopScopes is empty on purpose: the handoff issues a SESSION, which is
// the account itself signed in. A session carries no scope ceiling, so the
// registration states none -- and resolveRequestedScopes then refuses any
// request that asks for one with invalid_scope.
const DesktopScopes = ""

// ControlCLIScopes is the ceiling the control CLI's seeded registration
// carries. It is a constant of the build for the same reason the redirect
// address is: a credential minted for the built-in CLI authenticates against
// exactly this set, and a migration that edits one dialect's copy without the
// others drifts silently -- the store test pins the seeded rows to these
// constants so it cannot.
const ControlCLIScopes = "account:read account:write workspace:read workspace:write " +
	"worker:read worker:admin agent:read agent:write terminal:read terminal:write " +
	"file:read git:read git:write tunnel:open admin:read admin:users admin:settings admin:workers admin:apps"

// ControlCLIGrantTypes is the flow set the CLI's seeded registration runs.
// See ControlCLIScopes for why it is a constant.
const ControlCLIGrantTypes = "authorization_code refresh_token urn:ietf:params:oauth:grant-type:device_code"

// The three grant types /oauth/token serves, as RFC-defined wire identifiers.
// They live in this package beside the built-in ids because it is the
// dependency-free constants home both the hub's server and the CLI's client
// already import -- one spelling instead of one per side of the wire.
const (
	GrantTypeAuthorizationCode = "authorization_code"
	GrantTypeDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
	GrantTypeRefreshToken      = "refresh_token"
)

// ServiceAccountScopes is the ceiling the administrator-issued credential's
// seeded registration carries: the whole grantable vocabulary, so an
// out-of-band mint is limited by the issuing administrator alone. See
// ControlCLIScopes for why it is a constant.
const ServiceAccountScopes = ControlCLIScopes

// ControlCLIRedirectURI is the loopback redirect the control CLI registers.
// The port is absent on purpose; see MatchRedirectURI.
const ControlCLIRedirectURI = "http://127.0.0.1/callback"

// ControlCLIName and ServiceAccountName are what the consent screen and the
// credential list show.
const (
	ControlCLIName     = "LeapMux control CLI"
	ServiceAccountName = "Administrator-issued credential"
)

// DesktopClientID is the built-in desktop app: the sidecar's browser sign-in
// and step-up, whose code mints a Hub session rather than an app credential.
//
// Its one registered redirect URI is the same RFC 8252 section 7.3 loopback
// form as the CLI's: the sidecar's ephemeral listener matches on scheme, host
// and path with the port free.
const DesktopClientID = "leapmux-desktop"

// DesktopName is what the consent screen and the credential list show for the
// desktop app.
const DesktopName = "LeapMux Desktop"

// DesktopRedirectURI is the loopback redirect the desktop app registers. The
// port is absent on purpose; see ControlCLIRedirectURI.
const DesktopRedirectURI = "http://127.0.0.1/callback"

// IsBuiltIn reports whether a client id is one this build ships with. The
// built-in registrations cannot be deleted or edited, because their fields are
// constants of the build rather than rows an administrator owns.
func IsBuiltIn(clientID string) bool {
	return clientID == ControlCLIClientID || clientID == ServiceAccountClientID ||
		clientID == DesktopClientID
}

// BuiltInClientIDs lists them in a stable order, for a test or a listing that
// must cover all of them.
func BuiltInClientIDs() []string {
	return []string{ControlCLIClientID, ServiceAccountClientID, DesktopClientID}
}

// IssuesSession reports whether an authorization for this app mints a SESSION
// on the Hub rather than an app credential: the browser handoff of the
// desktop's remote-Hub mode. The consent page and the token exchange branch on
// it, and exactly one built-in answers true.
func IssuesSession(clientID string) bool {
	return clientID == DesktopClientID
}
