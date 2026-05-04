// Package verifycode generates and parses short, human-typeable email
// verification codes. The display form is "XXX-XXX" (e.g. "7XC-8DZ"); the
// storage form drops the hyphen ("7XC8DZ"). The 31-character alphabet
// excludes ambiguous glyphs (no 0/1, no I/O/L) so codes survive being read
// aloud or typed from a phone screen.
package verifycode

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Charset is the alphabet used for verification codes: digits 2-9 and
// uppercase letters A-Z minus I, O, and L. 31 characters total.
const Charset = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// Length is the number of characters in the storage form (no hyphen).
const Length = 6

// Generate returns a cryptographically random Length-character code in
// storage form. It panics if crypto/rand fails, matching the convention
// of util/id.Generate.
func Generate() string {
	max := big.NewInt(int64(len(Charset)))
	out := make([]byte, Length)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(fmt.Sprintf("verifycode: crypto/rand failed: %v", err))
		}
		out[i] = Charset[n.Int64()]
	}
	return string(out)
}

// Normalize converts user input to storage form. It uppercases letters,
// strips whitespace and hyphens, and verifies the result is Length chars
// drawn from Charset. Returns "" if the input does not normalize cleanly.
func Normalize(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		switch {
		case r == ' ' || r == '\t' || r == '-':
			continue
		case 'a' <= r && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		default:
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) != Length {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(Charset, rune(s[i])) {
			return ""
		}
	}
	return s
}

// Format inserts a hyphen at the midpoint to produce the display form.
// It assumes stored is already a valid storage-form code; callers that
// don't trust their input should run Normalize first.
func Format(stored string) string {
	if len(stored) != Length {
		return stored
	}
	return stored[:Length/2] + "-" + stored[Length/2:]
}
