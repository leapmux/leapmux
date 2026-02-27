package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPlanTitle(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		// Basic headings
		{"h1", "# My Plan", "My Plan"},
		{"h2", "## Implementation Details", "Implementation Details"},
		{"h3", "### Step 3 Notes", "Step 3 Notes"},
		{"h4", "#### Sub-heading", "Sub-heading"},
		{"h5", "##### Deep heading", "Deep heading"},
		{"h6", "###### Deepest heading", "Deepest heading"},
		{"no heading marker", "Just a plain title", "Just a plain title"},

		// Frontmatter skipping
		{"frontmatter", "---\ntitle: Plan\ntags: [a, b]\n---\n# Real Title", "Real Title"},
		{"empty frontmatter", "---\n---\n# Title After Empty Frontmatter", "Title After Empty Frontmatter"},
		{"frontmatter with blank lines after", "---\nfoo: bar\n---\n\n\n# Spaced Title", "Spaced Title"},

		// Bold formatting
		{"bold asterisks", "# **Bold Title**", "Bold Title"},
		{"bold underscores", "# __Bold Title__", "Bold Title"},

		// Italic formatting
		{"italic asterisks", "# *Italic Title*", "Italic Title"},
		{"italic underscores", "# _Italic Title_", "Italic Title"},

		// Strikethrough
		{"strikethrough", "# ~~Struck Title~~", "Struck Title"},

		// Inline code
		{"inline code", "# `Code Title`", "Code Title"},

		// Links
		{"markdown link", "# [Link Text](https://example.com)", "Link Text"},
		{"link with path", "# [Docs](./docs/README.md)", "Docs"},

		// Wiki links
		{"wiki link", "# [[Wiki Page]]", "Wiki Page"},

		// Image links
		{"image link", "# ![Alt Text](image.png)", "Alt Text"},

		// Nested formatting
		{"bold link", "# **[Bold Link](url)**", "Bold Link"},
		{"italic in bold", "# **_nested_ format**", "nested format"},
		{"code in heading", "## Use `extractTitle` function", "Use extractTitle function"},

		// HTML tags
		{"html tags", "# <em>HTML</em> Title", "HTML Title"},
		{"script tag", "# <script>alert('xss')</script>Clean", "Clean"},
		{"img onerror", `# <img onerror="alert(1)">Title`, "Title"},
		{"nested html", "# <b><i>Nested</i></b> HTML", "Nested HTML"},

		// Edge cases
		{"empty content", "", ""},
		{"whitespace only", "   \n  \n  ", ""},
		{"newlines only", "\n\n\n", ""},
		{"heading marker only", "#", "#"},
		{"heading with only space", "# ", "#"},

		// Long titles (truncation)
		{"truncation at 128", "# " + strings.Repeat("A", 200), strings.Repeat("A", 128)},
		{"exactly 128", "# " + strings.Repeat("B", 128), strings.Repeat("B", 128)},

		// Leading whitespace in content
		{"leading blank lines", "\n\n\n# Title After Blanks", "Title After Blanks"},
		{"indented content", "  # Indented Heading", "Indented Heading"},

		// Mixed scenarios
		{"real plan title", "---\nid: abc123\n---\n\n# Add authentication to the API\n\n## Overview\n...", "Add authentication to the API"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanTitle(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}
