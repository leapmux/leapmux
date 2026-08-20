package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/validate"
)

func TestExtractPlanTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty",
			content: "",
			want:    "",
		},
		{
			name:    "simple heading",
			content: "# Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "heading with bold",
			content: "# **Refactor auth middleware**",
			want:    "Refactor auth middleware",
		},
		{
			name:    "Plan: prefix",
			content: "# Plan: Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "Plan - prefix",
			content: "# Plan - Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "[Plan] prefix",
			content: "# [Plan] Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "plan: lowercase",
			content: "# plan: fix login bug",
			want:    "fix login bug",
		},
		{
			name:    "PLAN: uppercase",
			content: "# PLAN: Fix login bug",
			want:    "Fix login bug",
		},
		{
			name:    "Design prefix",
			content: "# Design: Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design Doc prefix",
			content: "# Design Doc: Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design Doc stripped before Design",
			content: "# Design Doc: API changes",
			want:    "API changes",
		},
		{
			name:    "design doc mixed case",
			content: "# dEsIgN dOc - API changes",
			want:    "API changes",
		},
		{
			name:    "wrapped Design Doc prefix",
			content: "# [Design Doc] API changes",
			want:    "API changes",
		},
		{
			name:    "wrapped Design prefix",
			content: "# (Design) Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design with em dash",
			content: "# Design — Migrate renderer",
			want:    "Migrate renderer",
		},
		{
			name:    "Plan with em dash",
			content: "# Plan — Migrate to new API",
			want:    "Migrate to new API",
		},
		{
			name:    "Plan with en dash",
			content: "# Plan – Migrate to new API",
			want:    "Migrate to new API",
		},
		{
			name:    "(Plan) prefix",
			content: "# (Plan) Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "*Plan* prefix",
			content: "# *Plan* Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "**Plan** prefix",
			content: "## **Plan** - Refactor auth",
			want:    "Refactor auth",
		},
		{
			name:    "{Plan} prefix",
			content: "# {Plan} Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "<Plan> prefix",
			content: "# <Plan> Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "no prefix left untouched",
			content: "# Implement caching layer",
			want:    "Implement caching layer",
		},
		{
			name:    "plan word in middle is not stripped",
			content: "# Implement plan caching",
			want:    "Implement plan caching",
		},
		{
			name:    "frontmatter skipped",
			content: "---\ntitle: test\n---\n# Plan: My title",
			want:    "My title",
		},
		{
			name:    "blank lines before heading",
			content: "\n\n# Plan: Real title",
			want:    "Real title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractPlanTitle(tt.content))
		})
	}
}

func TestSanitizePlanFilenameTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "lowercases ASCII and joins with hyphens",
			title: "Add Login Feature",
			want:  "add-login-feature",
		},
		{
			name:  "drops filesystem-reserved characters",
			title: `A/B\C:D*E?F"G<H>I|J`,
			want:  "abcdefghij",
		},
		{
			name:  "drops punctuation without inserting separators",
			title: "user's plan v2.0",
			want:  "users-plan-v20",
		},
		{
			name:  "preserves existing hyphens",
			title: "well-known issue",
			want:  "well-known-issue",
		},
		{
			name:  "collapses runs of hyphens and spaces",
			title: "Plan -- foo   bar",
			want:  "plan-foo-bar",
		},
		{
			name:  "trims leading and trailing separators",
			title: "  !!! Plan Name.  ",
			want:  "plan-name",
		},
		{
			name:  "trims leading and trailing hyphens",
			title: "---plan---",
			want:  "plan",
		},
		{
			name:  "trims mixed leading and trailing punctuation and hyphens",
			title: "-!- plan -!-",
			want:  "plan",
		},
		{
			name:  "falls back when empty",
			title: " \t\r\n ",
			want:  "untitled-plan",
		},
		{
			name:  "falls back when only special characters",
			title: "!@#$%^&*()",
			want:  "untitled-plan",
		},
		{
			name:  "preserves CJK letters (no case to fold)",
			title: "설계 문서 渲染修复",
			want:  "설계-문서-渲染修复",
		},
		{
			name:  "lowercases non-ASCII letters where applicable",
			title: "ÄPFEL Über",
			want:  "äpfel-über",
		},
		{
			name:  "strips control characters",
			title: "Plan\t\x00  Name\n\r",
			want:  "plan-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizePlanFilenameTitle(tt.title))
		})
	}
}

// TestExtractPlanTitleCapsBytes pins the cap that extractPlanTitle applies to
// the title it derives from an agent's own plan output. The cap counts UTF-8
// bytes, so it matches validate.SanitizeName, which the user-set title path
// applies to the same `title` column. A rune count let a Hangul or CJK title
// reach that column at three times the limit.
func TestExtractPlanTitleCapsBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		wantBytes int
		wantRunes int
	}{
		{
			name:      "128 ASCII bytes pass unchanged",
			content:   strings.Repeat("a", 128),
			wantBytes: 128,
			wantRunes: 128,
		},
		{
			name:      "129 ASCII bytes cut to 128",
			content:   strings.Repeat("a", 129),
			wantBytes: 128,
			wantRunes: 128,
		},
		{
			// 128 Hangul characters are 384 bytes. A rune count of 128
			// accepted all three hundred and eighty four.
			name:      "128 Hangul characters cut to 42",
			content:   strings.Repeat("한", 128),
			wantBytes: 126,
			wantRunes: 42,
		},
		{
			// Under 128 characters and over 128 bytes: the case that a rune
			// count never catches.
			name:      "50 Hangul characters cut to 42",
			content:   strings.Repeat("한", 50),
			wantBytes: 126,
			wantRunes: 42,
		},
		{
			name:      "64 two-byte characters fit exactly",
			content:   strings.Repeat("é", 64),
			wantBytes: 128,
			wantRunes: 64,
		},
		{
			name:      "65 two-byte characters cut to 64",
			content:   strings.Repeat("é", 65),
			wantBytes: 128,
			wantRunes: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractPlanTitle(tt.content)
			assert.Equal(t, tt.wantBytes, len(got), "byte length")
			assert.Equal(t, tt.wantRunes, utf8.RuneCountInString(got), "rune count")
			assert.True(t, utf8.ValidString(got), "the cut must land on a rune boundary")
		})
	}
}

// TestExtractPlanTitleKeepsTheWholeBudgetBehindAPrefix pins the reason
// extractPlanTitle calls CleanNameChars with NO scan limit.
//
// The prefix strip runs AFTER the character rule, so a character rule that
// stopped at the byte limit would hand the strip a result that already fills
// it, and the title would come back as many bytes shorter as the prefix was
// long. The bound belongs on the INPUT line instead, well above the title
// limit, which is what planLineScanLimit is.
//
// Without the fix, "# Plan: " + 128 letters returned 123 bytes rather than
// 128, and the plan file name shortened with it.
func TestExtractPlanTitleKeepsTheWholeBudgetBehindAPrefix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		content   string
		wantBytes int
		wantRunes int
	}{
		{"a Plan prefix costs the title nothing", "# Plan: " + strings.Repeat("A", 400), 128, 128},
		{"a Design Doc prefix costs the title nothing", "# Design Doc: " + strings.Repeat("A", 400), 128, 128},
		{"a bracketed prefix costs the title nothing", "# [Design Doc] " + strings.Repeat("A", 400), 128, 128},
		{"a prefix costs a Hangul title nothing", "# Plan: " + strings.Repeat("한", 400), 126, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractPlanTitle(tc.content)
			assert.Equal(t, tc.wantBytes, len(got), "byte length")
			assert.Equal(t, tc.wantRunes, utf8.RuneCountInString(got), "rune count")
			assert.NotContains(t, got, "Plan", "the prefix must be gone")
			assert.NotContains(t, got, "Design", "the prefix must be gone")
		})
	}
}

// TestExtractPlanTitleBoundsTheLine pins the input bound. A plan file is
// written by a model, so nothing upstream caps one line of it, and seven
// regexes, an HTML sanitizer and an entity decode all run on whatever length
// arrives.
//
// The bound must not change the ANSWER for any line a plan realistically
// carries, so it sits far above the title limit: a 4 KiB line still yields the
// full 128-byte title.
func TestExtractPlanTitleBoundsTheLine(t *testing.T) {
	t.Parallel()

	huge := "# Plan: " + strings.Repeat("A", 5_000_000)
	got := extractPlanTitle(huge)
	assert.Equal(t, strings.Repeat("A", 128), got,
		"a huge line still yields the full title, so the bound changes no realistic answer")
}

// TestExtractPlanTitleSatisfiesSanitizeName pins the one rule that both title
// paths obey. The auto-rename in worker/service writes extractPlanTitle's
// result to the same `title` column that a user-set title reaches through
// validate.SanitizeName. SanitizeName must therefore accept every derived
// title, and must return it unchanged.
func TestExtractPlanTitleSatisfiesSanitizeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "plain title",
			content: "# Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			// The characters the name rule used to strip. A plan title is
			// prose, and a price, a percentage and a quoted word belong in it.
			name:    "characters the name rule keeps",
			content: `# Plan: Ship $100 "raises" 50% \ now`,
			want:    `Ship $100 "raises" 50% \ now`,
		},
		{
			// The fold, on the one input that reaches it here: the markdown
			// strip leaves a double space where an emphasis marker was.
			name:    "a run of whitespace folds to one space",
			content: "# Plan:  Ship  the \t parser",
			want:    "Ship the parser",
		},
		{
			name:    "an invisible format character is stripped",
			content: "# \u202eShip\u200b the\u00ad parser",
			want:    "Ship the parser",
		},
		{
			// The pre-pass that hides a control character from rePlanPrefix
			// must not take the tab with it: CleanName folds a tab to a space,
			// and dropping it first gave "Shipthe parser".
			name:    "a tab between two words folds rather than vanishing",
			content: "# Ship\tthe parser",
			want:    "Ship the parser",
		},
		{
			// The same pre-pass still has to drop a control character INSIDE a
			// prefix, or rePlanPrefix stops matching it.
			name:    "a control character inside the prefix still hides nothing",
			content: "# Pl\x00an: Ship the parser",
			want:    "Ship the parser",
		},
		{
			// An INVISIBLE format character hides the prefix the same way a
			// control character does, and the local pre-pass this function used
			// to run could not see one: rePlanPrefix matched on the raw text,
			// missed "Plan", and CleanName then stripped U+200B afterwards --
			// so the stored title kept the "Plan: " the function exists to
			// remove. The shared character rule now runs BEFORE the match.
			name:    "a zero width space inside the prefix hides nothing",
			content: "# Pl\u200ban: Ship the parser",
			want:    "Ship the parser",
		},
		{
			name:    "a soft hyphen inside the prefix hides nothing",
			content: "# Pl\u00adan: Ship the parser",
			want:    "Ship the parser",
		},
		{
			name:    "a byte order mark inside the prefix hides nothing",
			content: "# Pl\ufeffan: Ship the parser",
			want:    "Ship the parser",
		},
		{
			// html.UnescapeString runs one line above the character rule, so it
			// can MANUFACTURE an invisible character that the prefix match then
			// has to survive.
			name:    "an entity-encoded zero width space inside the prefix hides nothing",
			content: "# Pl&#x200b;an: Ship the parser",
			want:    "Ship the parser",
		},
		{
			name:    "an entity-encoded soft hyphen inside the prefix hides nothing",
			content: "# Pl&shy;an: Ship the parser",
			want:    "Ship the parser",
		},
		{
			// An INVALID BYTE is stripped rather than grown into a visible
			// replacement character. strings.Map decoded it as U+FFFD and wrote
			// all 3 bytes, and CleanName deliberately KEEPS a real U+FFFD (it
			// decodes with size 3), so the title carried a visible "" into the
			// tab strip and into the plan file name -- while CleanName's own
			// test asserted the byte was dropped. Two title paths, two answers,
			// for one input.
			name:    "an invalid byte is dropped, not grown into a replacement character",
			content: "# Fix\xffparser",
			want:    "Fixparser",
		},
		{
			name:    "an invalid byte inside the prefix hides nothing",
			content: "# Pl\xffan: Ship the parser",
			want:    "Ship the parser",
		},
		{
			// A TAB is control AND whitespace, so it folds to a visible space
			// rather than vanishing -- which is what keeps "Ship\tthe parser"
			// from reading as "Shipthe parser". The cost is that a tab inside
			// the prefix leaves "Pl an:", which is not the word "plan", so the
			// whole line stays. That is the coherent answer, and this case pins
			// it: the exempted branch had no coverage at all.
			name:    "a tab inside the prefix leaves the line whole",
			content: "# Pl\tan: Ship the parser",
			want:    "Pl an: Ship the parser",
		},
		{
			name:    "a tab between two words folds to one space",
			content: "# Ship\tthe parser",
			want:    "Ship the parser",
		},
		{
			name:    "long ASCII title",
			content: strings.Repeat("a", 400),
			want:    strings.Repeat("a", 128),
		},
		{
			name:    "long Hangul title",
			content: strings.Repeat("한", 400),
			want:    strings.Repeat("한", 42),
		},
		{
			// The cut lands right after a space, and SanitizeName trims it.
			// The result is 127 bytes, not 128.
			name:    "cut next to a space loses the space",
			content: strings.Repeat("a", 127) + " bbb",
			want:    strings.Repeat("a", 127),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractPlanTitle(tt.content)
			assert.Equal(t, tt.want, got)

			sanitized, err := validate.SanitizeName(got)
			require.NoError(t, err, "SanitizeName must accept a derived title")
			assert.Equal(t, got, sanitized, "SanitizeName must return a derived title unchanged")
		})
	}
}

// TestExtractPlanTitleDropsFullyStrippedTitle covers the branch where nothing
// survives the character rule. The cut runs last, so the only empty result left
// is one whose every character was stripped or trimmed, and "" tells the caller
// to keep the title it already holds. A user who sets the same string gets
// SanitizeName's own refusal, so neither path writes it.
func TestExtractPlanTitleDropsFullyStrippedTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "only stripped characters", content: "# \u200b\ufeff\u00ad\u2060"},
		{name: "long run of stripped characters", content: "# " + strings.Repeat("\u200b", 400)},
		{name: "no content", content: ""},
		{name: "only whitespace", content: "  \n\t\n  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, extractPlanTitle(tt.content))
		})
	}
}

// TestPlanFilenameStemFitsNameMax pins the second reason that extractPlanTitle
// caps bytes. The title becomes a plan file name stem, and Linux caps one name
// component at NAME_MAX = 255 bytes. macOS hides a regression here, because
// APFS caps the component at 255 characters and accepts a 384-byte Hangul name.
func TestPlanFilenameStemFitsNameMax(t *testing.T) {
	t.Parallel()

	const nameMax = 255
	// The longest suffix that writePlanFile appends is ".<n>.md".
	const longestSuffix = ".999.md"

	tests := []struct {
		name    string
		content string
	}{
		{name: "long Hangul", content: strings.Repeat("한", 400)},
		{name: "long ASCII", content: strings.Repeat("a", 400)},
		{name: "long mixed", content: strings.Repeat("한 a ", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stem := SanitizePlanFilenameTitle(extractPlanTitle(tt.content))
			assert.LessOrEqual(t, len(stem)+len(longestSuffix), nameMax,
				"the plan file name must fit NAME_MAX on Linux")
		})
	}
}

// A plan stem must never resolve to a DOS device.
//
// `writePlanFile` joins this stem with ".md" and opens it directly, and Windows
// resolves a device name in any directory and with any extension -- so `con.md`
// reaches the console device rather than a file and the plan is lost. The retry
// suffix does not help either: `con.2.md` still reduces to CON.
func TestSanitizePlanFilenameTitleAvoidsWindowsDeviceNames(t *testing.T) {
	t.Parallel()

	for _, title := range []string{"CON", "Aux", "prn", "NUL", "COM1", "lpt9"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			stem := SanitizePlanFilenameTitle(title)
			assert.Falsef(t, validate.IsReservedDeviceName(stem),
				"%q produced the reserved stem %q", title, stem)
			// Still recognisable: the title is kept and only disambiguated.
			assert.Containsf(t, stem, strings.ToLower(title), "%q lost its text", title)
		})
	}
}

// An ordinary title keeps its stem exactly. The device guard must not rewrite a
// name that was never a device.
func TestSanitizePlanFilenameTitleLeavesAnOrdinaryTitleAlone(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "control-flow", SanitizePlanFilenameTitle("Control Flow"))
	// "console" merely STARTS with a device name; only the whole stem counts.
	assert.Equal(t, "console", SanitizePlanFilenameTitle("Console"))
}

// The plan-title path and the name rule cut bytes with ONE helper, so a guard
// added to one cannot go missing from the other. The copy that used to live in
// this file arrived WITHOUT the `limit <= 0` guard the original had, which is
// the evidence against "kept byte-identical by hand".
func TestPlanTitleUsesTheSharedByteCut(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		assert.Empty(t, validate.TruncateToBytes("hello", -1))
		assert.Empty(t, validate.TruncateToBytes("hello", 0))
	})
	// 'h' is one byte and 'e' is two, so a limit of 1 or 2 both stop after 'h'
	// rather than splitting the second rune.
	assert.Equal(t, "h", validate.TruncateToBytes("héllo", 1))
	assert.Equal(t, "h", validate.TruncateToBytes("héllo", 2))
	assert.Equal(t, "hé", validate.TruncateToBytes("héllo", 3))
}
