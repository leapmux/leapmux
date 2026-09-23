package agent

import (
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/util/validate"
)

// SanitizePlanFilenameTitle converts a plan title into a kebab-case filename
// stem: Unicode letters (Latin, CJK, Hangul, Cyrillic, ...) are lowercased
// and kept, digits are kept, whitespace becomes `-`, and everything else is
// dropped. Runs of `-` collapse to one, and leading/trailing `-` are trimmed.
func SanitizePlanFilenameTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	prevHyphen := false
	for _, r := range title {
		var out rune
		switch {
		case unicode.IsLetter(r):
			out = unicode.ToLower(r)
		case unicode.IsDigit(r):
			out = r
		case r == '-' || unicode.IsSpace(r):
			out = '-'
		default:
			continue
		}
		if out == '-' {
			if prevHyphen {
				continue
			}
			prevHyphen = true
		} else {
			prevHyphen = false
		}
		b.WriteRune(out)
	}
	stem := strings.Trim(b.String(), "-")
	if stem == "" {
		return "untitled-plan"
	}
	// A plan titled "CON", "Aux" or "COM1" reduces to a DOS device name, and
	// `writePlanFile` joins this stem with ".md" and opens it directly. Windows
	// resolves a device name in ANY directory and with ANY extension, so the
	// plan would go to the console device instead of a file -- and the retry
	// suffix would not help, because `con.2.md` still reduces to CON. The suffix
	// cannot itself be reserved, since every device name is one word.
	if validate.IsReservedDeviceName(stem) {
		return stem + "-plan"
	}
	return stem
}
