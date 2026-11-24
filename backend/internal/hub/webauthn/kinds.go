package webauthn

import "fmt"

// Kind identifies which WebAuthn ceremony a session belongs to. SessionStore
// rejects KindUnspecified so a ceremony that never stated its kind fails at
// creation rather than becoming a session that the wrong Finish call can
// consume.
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
type Kind int

const (
	KindUnspecified Kind = 0
	KindSignup      Kind = 1
	KindLogin       Kind = 2
	KindRegister    Kind = 3
	KindElevation   Kind = 4
	KindRecovery    Kind = 5
)

// IsValid reports whether k is one of the defined non-zero ceremony kinds.
func (k Kind) IsValid() bool {
	return k >= KindSignup && k <= KindRecovery
}

// String returns a human-readable label for logging and errors.
func (k Kind) String() string {
	switch k {
	case KindSignup:
		return "signup"
	case KindLogin:
		return "login"
	case KindRegister:
		return "register"
	case KindElevation:
		return "elevation"
	case KindRecovery:
		return "recovery"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}
