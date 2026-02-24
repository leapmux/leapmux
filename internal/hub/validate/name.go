package validate

import (
	"fmt"
	"strings"
)

// SanitizeName sanitizes and validates a name/title string.
// Forbidden characters (control characters, " and \) are silently stripped.
// Returns the sanitized name or an error if the result is empty or exceeds 64 characters.
func SanitizeName(name string) (string, error) {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7F && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized == "" {
		return "", fmt.Errorf("name must not be empty")
	}
	if len(sanitized) > 64 {
		return "", fmt.Errorf("name must be at most 64 characters")
	}
	return sanitized, nil
}
