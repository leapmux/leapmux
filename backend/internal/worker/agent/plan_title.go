package agent

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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

// planLineScanLimit caps the first line of a plan before extractPlanTitle
// reads it.
//
// A plan file is written by a model, so no length upstream bounds one line of
// it, and everything downstream costs time proportional to that length. The
// cap has to be well ABOVE validate.NameByteLimit and not equal to it,
// because the passes below REMOVE from the line -- the markdown syntax, the
// HTML tags, and the "Plan: " prefix -- and a title that fills the byte limit
// after those removals started out longer. 4 KiB holds any heading a plan
// carries, and it is small enough that the seven regexes and the HTML
// sanitizer stay cheap.
const planLineScanLimit = 4096

// truncateToBytes cuts s to at most limit UTF-8 bytes, moving the cut back to
// the start of a rune so the result never holds a partial rune. It repeats
// validate's unexported helper of the same name, because exporting that one
// would offer every caller a byte cut that ignores the character rule -- the
// exact order this file exists to get right.
func truncateToBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

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

	// Bound the line before the markdown passes run on it. Everything below --
	// seven regexes, the HTML sanitizer, the entity decode, and the character
	// rule -- costs time proportional to this length, and the plan file is
	// written by a model, so nothing upstream caps one line of it.
	//
	// The cut is on the INPUT rather than inside the character rule, and that
	// is what lets the title keep its full byte budget: the prefix strip below
	// removes bytes AFTER the clean, so a clean that stopped at the title
	// limit would hand the strip a result that is already at the limit, and
	// the title would come back as many bytes shorter as the prefix was long.
	line = truncateToBytes(line, planLineScanLimit)

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

	// Apply the shared character rule BEFORE the prefix match. rePlanPrefix
	// matches on the raw text, so any character that the reader cannot see
	// hides the prefix from it: "Pl\x00an: X" and "Pl​an: X" both keep
	// the "Plan: " that this function exists to remove. html.UnescapeString
	// above can manufacture the second one from "&#x200b;", one line before
	// the match.
	//
	// This calls CleanNameChars with NO scan limit, and not CleanName, because
	// CleanName also cuts to the byte limit: a cut here runs BEFORE the prefix
	// strip, and the prefix then pushes the same number of bytes off the end
	// of the title. The line is already bounded above, which is what makes an
	// unlimited scan safe here.
	//
	// A whitespace control folds to a space and does not vanish, so
	// "Ship\tthe parser" becomes "Ship the parser" and not "Shipthe parser".
	// A tab inside the prefix therefore still hides it -- "Pl\tan: X" reads as
	// "Pl an: X", which is not the word "plan", and keeping the whole line is
	// the correct answer for it.
	line = validate.CleanNameChars(line, 0)

	line = rePlanPrefix.ReplaceAllString(line, "")

	// CleanName runs the character rule again and then cuts to the byte limit,
	// so this path never has to report an error it has no user for. The rule
	// is idempotent, so the second pass only trims the space that the prefix
	// strip exposed and applies the cut. An empty result is what the caller
	// expects for a plan with no title.
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
