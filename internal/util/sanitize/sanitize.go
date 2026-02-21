package sanitize

import (
	"strings"
	"unicode"
)

// Title sanitizes a terminal title by removing control characters
// and limiting the length.
func Title(s string, maxLen int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if b.Len() >= maxLen {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
