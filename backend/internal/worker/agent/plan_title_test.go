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
			name:    "characters that SanitizeName strips",
			content: `# Plan: Ship $100 "raises" 50% \ now`,
			want:    "Ship 100 raises 50  now",
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

// TestExtractPlanTitleDropsFullyStrippedTitle covers the branch where
// SanitizeName refuses the derived title. Truncation runs first, so the only
// refusal left is an empty result, and "" tells the caller to keep the title it
// already holds. A user who sets the same string gets SanitizeName's own
// refusal, so neither path writes it.
func TestExtractPlanTitleDropsFullyStrippedTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "only stripped characters", content: `# $$"\%%`},
		{name: "long run of stripped characters", content: "# " + strings.Repeat("%", 400)},
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
