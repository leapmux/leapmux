package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeTerminal_EscapeEdgeCases(t *testing.T) {
	t.Run("does not eat newline after bare ESC or charset prefix", func(t *testing.T) {
		assert.Equal(t, "a\nb", SanitizeTerminalText("a\x1b\nb"))
		assert.Equal(t, "a\nb", SanitizeTerminalText("a\x1b(\nb"))
		assert.Equal(t, "a b", SanitizeSingleLine("a\x1b\nb"))
		assert.Equal(t, "a b", SanitizeSingleLine("a\x1b(\nb"))
	})

	t.Run("does not split multi-byte UTF-8 after bare ESC or charset prefix", func(t *testing.T) {
		assert.Equal(t, "é", SanitizeTerminalText("\x1bé"))
		assert.Equal(t, "é", SanitizeTerminalText("\x1b(é"))
		assert.Equal(t, "café", SanitizeSingleLine("caf\x1bé"))
	})

	t.Run("does not eat a second ESC before a CSI sequence", func(t *testing.T) {
		assert.Equal(t, "red", SanitizeTerminalText("\x1b\x1b[31mred\x1b[0m"))
		assert.Equal(t, "red", SanitizeTerminalText("\x1b(\x1b[31mred\x1b[0m"))
	})

	t.Run("still strips valid 2-byte and 3-byte ECMA-48 sequences", func(t *testing.T) {
		assert.Equal(t, "hello", SanitizeTerminalText("\x1b(Bhello"))
		assert.Equal(t, "hello", SanitizeTerminalText("\x1b7hello"))
		assert.Equal(t, "hello", SanitizeTerminalText("\x1bMhello"))
	})

	t.Run("handles truncated escape sequences at end of string", func(t *testing.T) {
		cases := map[string]string{
			"trailing bare ESC":                "abc\x1b",
			"truncated CSI opener":             "abc\x1b[",
			"truncated CSI params":             "abc\x1b[38;5;196",
			"truncated OSC":                    "abc\x1b]0;window title",
			"truncated OSC ending in bare ESC": "abc\x1b]0;window title\x1b",
			"truncated DCS":                    "abc\x1bPpayload",
			"truncated APC":                    "abc\x1b_payload",
			"truncated PM":                     "abc\x1b^payload",
			"truncated SOS":                    "abc\x1bXpayload",
			"truncated charset":                "abc\x1b(",
			"truncated 8-bit C1 CSI":           "abc\xc2\x9b31;1",
			"truncated 8-bit C1 DCS":           "abc\xc2\x90payload",
			"truncated 8-bit C1 SOS":           "abc\xc2\x98payload",
			"truncated 8-bit C1 PM":            "abc\xc2\x9epayload",
			"truncated 8-bit C1 APC":           "abc\xc2\x9fpayload",
			"standalone 8-bit C1 control":      "abc\xc2\x85",
		}
		for name, in := range cases {
			assert.Equal(t, "abc", SanitizeTerminalText(in), name)
			assert.Equal(t, "abc", SanitizeSingleLine(in), name)
		}
	})

	t.Run("strips 7-bit ST string sequences, C0 controls, CR, and BiDi marks", func(t *testing.T) {
		assert.Equal(t, "ok", SanitizeTerminalText("\x1b]0;title\x1b\\ok"))
		assert.Equal(t, "ok", SanitizeTerminalText("\x1bP1$r\x1b\\ok"))
		assert.Equal(t, "ab", SanitizeTerminalText("a\x00\x08\x7fb"))
		assert.Equal(t, "ab", SanitizeTerminalText("a\rb"))
		assert.Equal(t, "a b", SanitizeSingleLine("a\rb"))
		assert.Equal(t, "clean", SanitizeTerminalText("\u061c\u200e\u200f\u2029\u202a\u202b\u202c\u202d\u2067\u2068\u2069clean"))
	})
}

func TestSanitizeTerminal_CleanASCIIFastPath(t *testing.T) {
	clean := "workspace-123: ready (42ms)"
	assert.Equal(t, clean, SanitizeTerminalText(clean))
	assert.Equal(t, clean, SanitizeSingleLine(clean))

	multiline := "line 1\n\tline 2"
	assert.Equal(t, multiline, SanitizeTerminalText(multiline))
	assert.Equal(t, "line 1  line 2", SanitizeSingleLine(multiline))

	allocsText := testing.AllocsPerRun(50, func() {
		_ = SanitizeTerminalText(multiline)
	})
	assert.Zero(t, allocsText, "clean multi-line ASCII must not allocate")

	allocsSingle := testing.AllocsPerRun(50, func() {
		_ = SanitizeSingleLine(clean)
	})
	assert.Zero(t, allocsSingle, "clean single-line ASCII must not allocate")
}
