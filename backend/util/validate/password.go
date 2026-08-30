package validate

import (
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The printable ASCII range, and the whole character set a password may hold.
// 0x20 is the space and 0x7E is the tilde.
const (
	minPrintableASCII = contracts.MinPrintableASCII
	maxPrintableASCII = contracts.MaxPrintableASCII
)

// Password length limits, counted in characters. A password holds printable
// ASCII characters only (see ValidatePassword), so one character is one byte
// and `len` counts characters here.
const (
	MinPasswordLength = contracts.MinPasswordLength
	MaxPasswordLength = contracts.MaxPasswordLength
)

// ValidatePassword checks that a password meets the character-set policy and
// the length policy. Returns an error describing the problem, or nil if
// valid.
//
// A password holds printable ASCII characters only: 0x20 (the space) through
// 0x7E (the tilde). The rule has two halves, and each half has its own
// reason.
//
// The upper half refuses every character above 0x7E, because the hub and the
// browser cannot otherwise agree on what a length limit counts: Go's `len`
// counts UTF-8 BYTES and JavaScript's `String.length` counts UTF-16 CODE
// UNITS. A 43 character CJK password is 43 code units and 129 bytes, so the
// browser copy in `frontend/src/lib/validate.ts` accepted it and this
// function then refused it as too long -- with a message that said
// "characters", which is neither count. ASCII makes one character one byte
// and one code unit at the same time, so the two limits become one rule.
//
// The lower half refuses the control block 0x00-0x1F and DEL (0x7F). It is
// narrower than the length reason alone requires. A control character reaches
// a password field through a paste accident or a terminal control sequence,
// never through deliberate typing, and a password that the user cannot type
// again is a lockout. The space (0x20) stays ALLOWED: a passphrase with
// spaces is a good password, and neither side trims a password.
//
// testdata/password_policy_conformance.json is the fixture both sides run,
// and it pins each boundary of this range.
//
// ValidatePassword runs where a password is SET or CHANGED, never where one
// is verified. A stored password is checked by pwdhash.Verify alone, so this
// policy never refuses a credential that the hub already accepted.
func ValidatePassword(password string) error {
	// The character-set rule runs FIRST, because its refusal is the
	// actionable one. A user who counted 3 CJK characters cannot act on
	// "at least 8 characters" when this function counted 9 bytes.
	//
	// The loop reads BYTES, which is the invariant itself: every byte in
	// 0x20-0x7E is one whole printable ASCII character, so a string that
	// passes holds one byte for each character. Ranging over runes would
	// decode UTF-8 first and answer the same question less directly.
	for i := 0; i < len(password); i++ {
		if password[i] < minPrintableASCII || password[i] > maxPrintableASCII {
			return fmt.Errorf("password must contain only printable ASCII characters (the space is allowed)")
		}
	}
	n := len(password)
	if n < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if n > MaxPasswordLength {
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	}
	return nil
}
