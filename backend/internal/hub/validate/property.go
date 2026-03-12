package validate

import (
	"fmt"
	"regexp"
)

var propertyInvalidChars = regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)

// SanitizeProperty removes characters not in [a-zA-Z0-9\-_.] from the value.
func SanitizeProperty(value string) string {
	return propertyInvalidChars.ReplaceAllString(value, "")
}

// ValidateProperty sanitizes the value and returns an error if the result is empty.
// The fieldName parameter is used in the error message for clarity.
func ValidateProperty(fieldName, value string) (string, error) {
	sanitized := SanitizeProperty(value)
	if sanitized == "" {
		return "", fmt.Errorf("%s must not be empty after removing invalid characters (allowed: a-z A-Z 0-9 - _ .)", fieldName)
	}
	return sanitized, nil
}
