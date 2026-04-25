package validate

import (
	"fmt"
	"net/mail"
	"strings"
)

// MaxEmailLength is the maximum allowed length for an email address.
const MaxEmailLength = 254

// ValidateEmail checks that the given email address is well-formed.
// It returns an error describing the problem, or nil if the address is valid.
// Empty strings are accepted (use a separate required check if needed).
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}

	if len(email) > MaxEmailLength {
		return fmt.Errorf("email must be at most %d characters", MaxEmailLength)
	}

	addr, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("invalid email address")
	}

	// mail.ParseAddress accepts "Name <user@host>" — reject display names.
	if addr.Address != email {
		return fmt.Errorf("invalid email address")
	}

	// Require a dot in the domain part (rejects "user@localhost").
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || !strings.Contains(parts[1], ".") {
		return fmt.Errorf("invalid email address")
	}

	return nil
}
