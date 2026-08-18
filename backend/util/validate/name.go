package validate

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NameByteLimit is the maximum size of a name or a title, in UTF-8 bytes.
//
// The limit counts UTF-8 bytes, because `len` counts bytes. 128 bytes holds
// 128 plain ASCII characters, but only approximately 42 CJK characters. The
// browser copy in `frontend/src/lib/validate.ts` counts the same bytes and
// states the same limit. A name that the field accepts is therefore a name
// that this package accepts.
const NameByteLimit = 128

// zeroWidthNoBreakSpace (U+FEFF, the byte order mark) is stripped although
// unicode.IsControl reports false for it: its category is Cf, not Cc.
//
// It is here because it is the one input on which this rule and its browser
// copy disagreed. JavaScript's String.prototype.trim removes U+FEFF and Go's
// strings.TrimSpace keeps it, so a pasted title that carried a byte order mark
// gave the browser "Plan" and this package a "Plan" that still holds the mark.
// An invisible mark carries no meaning in a name, so both sides now strip it,
// and the shared fixture testdata/title_cleaning_conformance.json holds the
// case.
const zeroWidthNoBreakSpace = '\uFEFF'

// SanitizeName sanitizes and validates a name/title string.
// Forbidden characters (control characters, the byte order mark, ", \, $, %)
// are silently stripped. Returns the sanitized name or an error if the result
// is empty or exceeds NameByteLimit bytes.
//
// Use CleanName instead at a write point that must accept whatever the caller
// sends, such as a tab title. This function is for a field whose error a user
// reads and corrects.
func SanitizeName(name string) (string, error) {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if !unicode.IsControl(r) && r != '"' && r != '\\' && r != '$' && r != '%' && r != zeroWidthNoBreakSpace {
			b.WriteRune(r)
		}
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized == "" {
		return "", fmt.Errorf("name must not be empty")
	}
	if len(sanitized) > NameByteLimit {
		return "", fmt.Errorf("name must be at most %d bytes", NameByteLimit)
	}
	return sanitized, nil
}

// CleanName returns name in the form that SanitizeName accepts. It never
// fails: it cuts name to NameByteLimit bytes, then applies the SanitizeName
// character rule to what remains. An empty return value means that no
// character survived -- the name was empty, or it held only whitespace and
// forbidden characters -- and each caller decides its own fallback for that
// case.
//
// The order is load-bearing. SanitizeName only ever REMOVES bytes, so a string
// that already fits the limit still fits after the strip. That makes the "too
// long" error unreachable here, and it makes this function idempotent:
// CleanName(CleanName(s)) == CleanName(s).
//
// Every writer of a tab title calls this, in the worker and in the browser
// alike, so the three title-writing RPCs and the derived plan title enforce
// one rule and none of them refuses a title. The browser copy is `cleanName`
// in `frontend/src/lib/validate.ts`; testdata/title_cleaning_conformance.json
// pins the two against each other.
func CleanName(name string) string {
	cleaned, err := SanitizeName(truncateToBytes(name, NameByteLimit))
	if err != nil {
		return ""
	}
	return cleaned
}

// truncateToBytes cuts s to at most limit UTF-8 bytes. It moves the cut back
// to the start of a rune, so the result never holds a partial rune. A limit of
// zero or less keeps nothing.
func truncateToBytes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
