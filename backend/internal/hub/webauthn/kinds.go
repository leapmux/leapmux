package webauthn

// Ceremony session kinds stored in webauthn_sessions.kind.
//
// KindElevation is the step-up assertion: proving a passkey to elevate an
// already signed-in session. It mints no artefact of its own -- the session
// row carries the elevation -- so there is no second "proof" kind to expire,
// consume, or leak.
const (
	KindSignup    = "signup"
	KindLogin     = "login"
	KindRegister  = "register"
	KindElevation = "elevation"
)
