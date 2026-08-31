package webauthn

// Ceremony session kinds stored in webauthn_sessions.kind.
//
// KindElevation is the step-up assertion: proving a passkey to elevate an
// already signed-in session. It mints no artefact of its own -- the session
// row carries the elevation -- so there is no second "proof" kind to expire,
// consume, or leak.
//
// KindRecovery is a registration that runs on an EXISTING account but
// without a signed-in session: the emailed account-recovery token replaces
// the session as the ceremony's authorization. Like KindRegister it is
// per-user, but unlike it the existing passkeys are about to be revoked, so
// the options carry no credential exclusions -- their descriptors would be
// handed to whoever holds the link.
const (
	KindSignup    = "signup"
	KindLogin     = "login"
	KindRegister  = "register"
	KindElevation = "elevation"
	KindRecovery  = "recovery"
)
