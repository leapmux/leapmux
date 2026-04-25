package validate

import "fmt"

// Password length limits.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 128
)

// ValidatePassword checks that a password meets the length policy.
// Returns an error describing the problem, or nil if valid.
func ValidatePassword(password string) error {
	n := len(password)
	if n < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if n > MaxPasswordLength {
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
	}
	return nil
}
