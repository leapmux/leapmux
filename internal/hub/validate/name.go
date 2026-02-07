package validate

import (
	"fmt"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9 _\-.]+$`)

// ValidateName validates a name/title string.
// Rules: trimmed non-empty, max 64 chars, only [a-zA-Z0-9 _\-.].
func ValidateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len(trimmed) > 64 {
		return fmt.Errorf("name must be at most 64 characters")
	}
	if !namePattern.MatchString(trimmed) {
		return fmt.Errorf("name must contain only letters, numbers, spaces, hyphens, underscores, and dots")
	}
	return nil
}
