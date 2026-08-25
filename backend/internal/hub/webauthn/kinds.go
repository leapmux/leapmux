package webauthn

// Ceremony session kinds stored in webauthn_sessions.kind.
const (
	KindSignup      = "signup"
	KindLogin       = "login"
	KindRegister    = "register"
	KindReauth      = "reauth"
	KindReauthProof = "reauth_proof"
)
