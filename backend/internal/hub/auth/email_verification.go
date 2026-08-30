package auth

import "github.com/leapmux/leapmux/internal/hub/store"

// EmailVerificationFacts carries the two ACCOUNT facts the verification gate
// reads. It is a value rather than two parameters, and that is the whole
// point of the type.
//
// The predicate took three adjacent booleans -- required, isAdmin,
// emailVerified -- so a call site that transposed two of them compiled,
// passed every type check, and inverted the gate in silence. Two of the five
// call sites read almost identically (an administrator flag and a verified
// flag, from a request message at one and from a locked row at the other),
// which is exactly the shape a transposition hides in. Named fields cannot
// be transposed without a reader seeing it.
//
// The POLICY input stays a parameter of Satisfied. It is a fact about the
// hub, not about the account, and each caller resolves it from its own
// settings snapshot.
type EmailVerificationFacts struct {
	// IsAdmin carries the administrator exemption. See Satisfied.
	IsAdmin bool
	// EmailVerified records whether somebody confirmed the address.
	EmailVerified bool
}

// EmailVerificationFactsFromUser reads the facts from a stored user row.
// Nil-safe: a nil row reports neither fact, which fails closed.
func EmailVerificationFactsFromUser(u *store.User) EmailVerificationFacts {
	if u == nil {
		return EmailVerificationFacts{}
	}
	return EmailVerificationFacts{IsAdmin: u.IsAdmin, EmailVerified: u.EmailVerified}
}

// EmailVerificationFacts reads the facts from the acting credential's
// resolved identity. Nil-safe for the same reason the constructor above is.
func (u *UserInfo) EmailVerificationFacts() EmailVerificationFacts {
	if u == nil {
		return EmailVerificationFacts{}
	}
	return EmailVerificationFacts{IsAdmin: u.IsAdmin, EmailVerified: u.EmailVerified}
}

// Satisfied reports whether the account may use the hub although the hub
// requires a verified email address.
//
// This is the ONE place that applies the administrator exemption, and applying
// it HERE rather than in the stored column is the whole point. email_verified
// records whether somebody confirmed the address, which is a fact about the
// address; "an administrator is never locked out of their own hub" is a fact
// about the account. Writing the second into the first made an
// administrator's unconfirmed address a valid self-service account-recovery
// target, because RequestAccountRecovery reads the column and cannot take this
// exemption -- the question it asks IS "did anybody confirm this address".
//
// Five call sites share it: the auth interceptor's per-request check, the two
// login legs that decide whether to send the user to /verify-email, and the
// two administrator verbs that refuse an address-less account the hub could
// never verify.
//
// It lives in its own file rather than inside the interceptor, because four
// of the five call sites are in the service layer and read it as a peer. A
// rule that every layer applies does not belong to the layer that happens to
// apply it first.
func (f EmailVerificationFacts) Satisfied(required bool) bool {
	return !required || f.IsAdmin || f.EmailVerified
}
