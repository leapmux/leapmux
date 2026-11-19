package oauthapp

// The two `step_up` target spellings an authorization request carries, and
// the one refusal code the device-code step-up answers for a browser-issued
// credential.
//
// They live here for the same reason the grant-type identifiers do: the Hub's
// parser, the control CLI and the desktop sidecar all read them, and this is
// the dependency-free constants home both sides of the wire already import.
const (
	// StepUpSessionPrefix introduces a session target: `session:<id>`. The
	// value after the prefix names a user_sessions row this authorization
	// VERIFIES rather than issues.
	StepUpSessionPrefix = "session:"
	// StepUpTokenPrefix introduces an app-credential target: `token:<id>`.
	StepUpTokenPrefix = "token:"
)

// BrowserStepUpRequiredCode is the OAuth error code /oauth/step-up answers for
// a credential a browser already issued. Such a credential proves its factor
// in the browser that holds its session -- the authorization-code handoff set
// that session up -- so the device-code ceremony would verify it against
// nothing a person is at.
//
// Distinct from `unsupported_credential`, which answers a credential that is
// not an app credential at all.
const BrowserStepUpRequiredCode = "browser_step_up_required"
