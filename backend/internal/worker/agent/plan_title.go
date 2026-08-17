package agent

import (
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/util/validate"
	"github.com/microcosm-cc/bluemonday"
)

var (
	reHeading       = regexp.MustCompile(`^#{1,6}\s+`)
	reBold          = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reItalic        = regexp.MustCompile(`\*(.+?)\*|_(.+?)_`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
	reInlineCode    = regexp.MustCompile("`(.+?)`")
	reImageLink     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink          = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reWikiLink      = regexp.MustCompile(`\[\[(.+?)\]\]`)

	// rePlanPrefix matches common plan/design prefixes in titles, e.g.
	// "Plan:", "Design:", "Design Doc -", "[Plan]", "(Design Doc)", etc.
	// The longer "Design Doc" prefix appears first so it wins over "Design".
	rePlanPrefix = regexp.MustCompile(`(?i)^[\[({<*]*(design\s+doc|design|plan)[\])}>*]*[\s:\-–—]+`)

	htmlPolicy = bluemonday.StrictPolicy()
)

// extractPlanTitle extracts a human-readable title from markdown plan content.
// It returns the first meaningful line, stripped of markdown formatting. An
// empty return value means that no title survived, and the caller keeps the
// title it already holds.
//
// The result always satisfies validate.SanitizeName unchanged. The worker
// writes this title to the same `title` column that a user-set title reaches
// through validate.CleanName, so both paths must enforce one rule.
//
// The byte limit also keeps the plan file writable. This title becomes the file
// name stem, and Linux caps one name component at NAME_MAX = 255 BYTES, so a
// title of 128 CJK or Hangul characters (384 bytes) failed the write with
// ENAMETOOLONG. macOS hides the failure: APFS caps the component at 255
// characters instead, and accepts the same name.
func extractPlanTitle(content string) string {
	// Skip YAML frontmatter.
	if strings.HasPrefix(content, "---\n") {
		if idx := strings.Index(content[4:], "\n---\n"); idx >= 0 {
			content = content[4+idx+5:]
		} else if strings.HasPrefix(content[4:], "---\n") {
			content = content[8:]
		}
	}

	// Find first non-empty line.
	var line string
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}

	// Strip heading markers.
	line = reHeading.ReplaceAllString(line, "")

	// Strip markdown inline formatting.
	line = reBold.ReplaceAllString(line, "${1}${2}")
	line = reItalic.ReplaceAllString(line, "${1}${2}")
	line = reStrikethrough.ReplaceAllString(line, "${1}")
	line = reInlineCode.ReplaceAllString(line, "${1}")
	line = reImageLink.ReplaceAllString(line, "${1}")
	line = reLink.ReplaceAllString(line, "${1}")
	line = reWikiLink.ReplaceAllString(line, "${1}")

	// Strip HTML tags.
	line = htmlPolicy.Sanitize(line)

	// Decode HTML entities.
	line = html.UnescapeString(line)

	// Clean up whitespace and control characters. SanitizeName strips control
	// characters again at the end of this function, and this earlier pass still
	// has to run: a control character inside a prefix ("Pl\x00an: X") hides that
	// prefix from rePlanPrefix below, which matches on the raw text.
	line = strings.TrimSpace(line)
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)

	line = rePlanPrefix.ReplaceAllString(line, "")
	line = strings.TrimSpace(line)

	// CleanName cuts the line to the byte limit and then applies the character
	// rule, so this path never has to report an error it has no user for. An
	// empty result is what the caller expects for a plan with no title.
	return validate.CleanName(line)
}

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
	return stem
}
