package validate

import (
	"fmt"
	"regexp"
	"strings"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// SanitizeSlug trims whitespace, lowercases, then validates as a
// GitHub-style slug (username or organization name).
// Rules: 1-32 chars, lowercase alphanumeric and hyphens only,
// no leading/trailing hyphens, no consecutive hyphens.
// Returns the cleaned slug and an error if invalid.
func SanitizeSlug(fieldName, value string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(value))
	if slug == "" {
		return "", fmt.Errorf("%s must not be empty", fieldName)
	}
	if len(slug) > 32 {
		return "", fmt.Errorf("%s must be at most 32 characters", fieldName)
	}
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("%s must contain only letters, numbers, and hyphens", fieldName)
	}
	if strings.HasPrefix(slug, "-") {
		return "", fmt.Errorf("%s must not start with a hyphen", fieldName)
	}
	if strings.HasSuffix(slug, "-") {
		return "", fmt.Errorf("%s must not end with a hyphen", fieldName)
	}
	if strings.Contains(slug, "--") {
		return "", fmt.Errorf("%s must not contain consecutive hyphens", fieldName)
	}
	return slug, nil
}
