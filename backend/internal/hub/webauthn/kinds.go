package webauthn

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Ceremony session kinds. webauthn_sessions.kind stores these ordinals, and its
// CHECK refuses the UNSPECIFIED zero -- so a ceremony that never stated its
// kind fails the insert rather than becoming a row that the wrong Finish call
// can consume.
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
	KindSignup    = leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_SIGNUP
	KindLogin     = leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_LOGIN
	KindRegister  = leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_REGISTER
	KindElevation = leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_ELEVATION
	KindRecovery  = leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_RECOVERY
)
