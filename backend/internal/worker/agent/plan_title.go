package agent

import (
	"html"
	"regexp"
	"strings"
	"unicode"

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
// It returns the first meaningful line, stripped of markdown formatting.
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

	// Clean up whitespace and control characters.
	line = strings.TrimSpace(line)
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)

	line = rePlanPrefix.ReplaceAllString(line, "")
	line = strings.TrimSpace(line)

	// Truncate to 128 characters.
	if len([]rune(line)) > 128 {
		line = string([]rune(line)[:128])
	}

	return line
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
