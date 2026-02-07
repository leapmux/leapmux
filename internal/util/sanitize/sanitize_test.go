package sanitize

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTitle(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty", "", 100, ""},
		{"normal", "bash", 100, "bash"},
		{"with control chars", "ba\x00sh\x07", 100, "bash"},
		{"truncate", "very long title", 8, "very lon"},
		{"trim whitespace", "  hello  ", 100, "hello"},
		{"unicode", "日本語タイトル", 100, "日本語タイトル"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Title(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got, "Title(%q, %d)", tt.input, tt.maxLen)
		})
	}
}
