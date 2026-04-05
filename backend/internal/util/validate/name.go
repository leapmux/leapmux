package validate

import (
	"fmt"
	"strings"
)

// SanitizeName sanitizes and validates a name/title string.
// Forbidden characters (control characters, ", \, $, %) are silently stripped.
// Returns the sanitized name or an error if the result is empty or exceeds 128 characters.
func SanitizeName(name string) (string, error) {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7F && r != '"' && r != '\\' && r != '$' && r != '%' {
			b.WriteRune(r)
		}
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized == "" {
		return "", fmt.Errorf("name must not be empty")
	}
	if len(sanitized) > 128 {
		return "", fmt.Errorf("name must be at most 128 characters")
	}
	return sanitized, nil
}

// SanitizeDisplayName sanitizes a display name, falling back to the given
// fallback value when the name is empty.
func SanitizeDisplayName(displayName, fallback string) (string, error) {
	if displayName == "" {
		displayName = fallback
	}
	return SanitizeName(displayName)
}

// ValidateSessionID validates a session ID for resuming an agent session.
// Empty values are accepted (no resume). Non-empty values are checked via
// SanitizeName; any character that SanitizeName would strip is rejected.
func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	sanitized, err := SanitizeName(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}
	if sanitized != sessionID {
		return fmt.Errorf("session ID contains invalid characters")
	}
	return nil
}
