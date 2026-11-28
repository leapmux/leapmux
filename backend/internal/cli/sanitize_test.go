package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		assert.Equal(t, "hello", SanitizeTerminalText("\x1b#8hello"))
		assert.Equal(t, "hello", SanitizeTerminalText("\x1b%Ghello"))
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

func streamSanitize(s *TerminalSanitizer, chunks ...string) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(s.Write([]byte(chunk)))
	}
	b.WriteString(s.Flush())
	return b.String()
}

func TestTerminalSanitizer_StreamingEdgeCases(t *testing.T) {
	t.Run("handles empty write", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Empty(t, s.Write(nil))
		assert.Empty(t, s.Write([]byte{}))
		assert.Empty(t, s.Flush())
	})

	t.Run("trailing ESC inside OSC splits ST and double-ESC ST cleanly", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "ab", streamSanitize(s, "a\x1b]0;title\x1b", "\\b"))

		s.Reset()
		assert.Equal(t, "ab", streamSanitize(s, "a\x1b]0;title\x1b", "\x1b\\b"))

		s.Reset()
		assert.Equal(t, "a", streamSanitize(s, "a\x1b]0;title\x1b"))
	})

	t.Run("inner CSI inside OSC stays inside OSC until terminator", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "beforeafter", streamSanitize(
			s,
			"before\x1b]0;title\x1b",
			"[31mred-inside-osc\x1b",
			"\\after",
		))

		s.Reset()
		assert.Equal(t, "beforeafter", streamSanitize(
			s,
			"before\x1b]0;title\x1b[31mstill-osc\x07after",
		))
	})

	t.Run("malformed CSI with newline aborts CSI and preserves newline", func(t *testing.T) {
		ml := NewTerminalSanitizer()
		assert.Equal(t, "before\nafter", streamSanitize(ml, "before\x1b[31", "\nafter"))
		assert.Equal(t, "before\nafter", streamSanitize(ml, "before\x1b[1 ", "\nafter"))

		sl := NewSingleLineTerminalSanitizer()
		assert.Equal(t, "before after", streamSanitize(sl, "before\x1b[31", "\nafter"))
		assert.Equal(t, "before after", streamSanitize(sl, "before\x1b[1 ", "\nafter"))
	})

	t.Run("strips ESC # 8 and ESC % G including across chunk boundaries", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "ok", streamSanitize(s, "\x1b", "#", "8\x1b%", "Gok"))
		assert.Equal(t, "ok", SanitizeTerminalText("\x1b#8\x1b%Gok"))
		assert.Equal(t, "ok", SanitizeSingleLine("\x1b#8\x1b%Gok"))
	})

	t.Run("strips 64 KiB split OSC 52 payload in constant parser memory", func(t *testing.T) {
		payload := strings.Repeat("A", 64*1024)
		for name, term := range map[string]string{
			"BEL":      "\x07",
			"7-bit ST": "\x1b\\",
			"8-bit ST": "\xc2\x9c",
		} {
			t.Run(name, func(t *testing.T) {
				s := NewTerminalSanitizer()
				var out strings.Builder
				out.WriteString(s.Write([]byte("pre\x1b]52;c;")))
				for off := 0; off < len(payload); off += 4096 {
					out.WriteString(s.Write([]byte(payload[off : off+4096])))
					assert.LessOrEqual(t, len(s.pending), 3)
				}
				out.WriteString(s.Write([]byte(term + "post")))
				out.WriteString(s.Flush())
				assert.Equal(t, "prepost", out.String())
			})
		}
	})

	t.Run("handles split 8-bit C1 sequences and valid U+00A0..U+00BF runes", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x9b31;1mb"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x9d0;title\xc2", "\x9cb"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x90dcs\x07b"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x98sos\x1b\\b"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x9epm\xc2\x9cb"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x9fapc\x07b"))
		assert.Equal(t, "ab", streamSanitize(s, "a\xc2", "\x85b"))
		assert.Equal(t, "a©b", streamSanitize(s, "a\xc2", "\xa9b"))
		assert.Equal(t, "a\uFFFDx", streamSanitize(s, "a\xc2", "x"))
	})

	t.Run("handles split 2-, 3-, and 4-byte UTF-8 and strips split BiDi runes", func(t *testing.T) {
		// Feed every byte as its own 1-byte chunk.
		input := "café" + "©" + "한" + "😀" + "\u061c" + "\u200e" + "\u2028" + "\u202e" + "\u2066" + "end"
		s := NewTerminalSanitizer()
		var out strings.Builder
		for i := 0; i < len(input); i++ {
			out.WriteString(s.Write([]byte{input[i]}))
		}
		out.WriteString(s.Flush())
		assert.Equal(t, "café©한😀end", out.String())
		assert.Equal(t, "café©한😀end", SanitizeTerminalText(input))
	})

	t.Run("single-line sanitizer replaces CR, LF, and TAB with spaces across chunks", func(t *testing.T) {
		sl := NewSingleLineTerminalSanitizer()
		assert.Equal(t, "a b  c d", streamSanitize(sl, "a\t", "b\r\n", "c\nd"))

		ml := NewTerminalSanitizer()
		assert.Equal(t, "a\tb\nc\nd", streamSanitize(ml, "a\t", "b\r\n", "c\nd"))
	})

	t.Run("Flush emits replacement runes for truncated UTF-8 and drops truncated escapes", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "a", s.Write([]byte("a\xc2")))
		assert.Equal(t, "\uFFFD", s.Flush())
		assert.Equal(t, "next", s.Write([]byte("next")))

		assert.Equal(t, "a", s.Write([]byte("a\xe2\x80")))
		assert.Equal(t, "\uFFFD\uFFFD", s.Flush())
		assert.Equal(t, "next", s.Write([]byte("next")))

		assert.Equal(t, "a", s.Write([]byte("a\x1b]0;unterminated")))
		assert.Equal(t, "", s.Flush())
		assert.Equal(t, "next", s.Write([]byte("next")))
	})

	t.Run("Reset clears state and pending bytes while retaining capacity and mode", func(t *testing.T) {
		s := NewSingleLineTerminalSanitizer()
		// Leave pending UTF-8 bytes and verify Reset keeps backing capacity.
		assert.Equal(t, "hi", s.Write([]byte("hi\xe2\x80")))
		require.Len(t, s.pending, 2)
		capBefore := cap(s.pending)
		require.GreaterOrEqual(t, capBefore, 4)

		s.Reset()
		assert.Empty(t, s.pending)
		assert.Equal(t, capBefore, cap(s.pending))
		assert.Equal(t, sanStateGround, s.state)

		// Leave parser inside an unterminated OSC, reset, and confirm single-line mode holds.
		assert.Equal(t, "a", s.Write([]byte("a\x1b]0;dangling")))
		s.Reset()
		assert.Equal(t, "x y", streamSanitize(s, "x\ny"))

		// Zero-value TerminalSanitizer also retains allocated pending capacity across Reset.
		var zero TerminalSanitizer
		assert.Empty(t, zero.Write([]byte("\xf0\x9f")))
		require.Len(t, zero.pending, 2)
		zeroCap := cap(zero.pending)
		require.Greater(t, zeroCap, 0)
		zero.Reset()
		assert.Empty(t, zero.pending)
		assert.Equal(t, zeroCap, cap(zero.pending))
	})

	t.Run("drains truncated 3- and 4-byte UTF-8 before reprocessing ASCII or ESC", func(t *testing.T) {
		s := NewTerminalSanitizer()
		assert.Equal(t, "\uFFFD\uFFFDx", streamSanitize(s, "\xe2\x80x"))
		assert.Equal(t, "\uFFFD\uFFFDx", streamSanitize(s, "\xe2\x80", "x"))
		assert.Equal(t, "\uFFFD\uFFFDok", streamSanitize(s, "\xe2\x80\x1b[31mok\x1b[0m"))
		assert.Equal(t, "\uFFFD\uFFFDok", streamSanitize(s, "\xe2\x80", "\x1b[31mok\x1b[0m"))

		assert.Equal(t, "\uFFFD\uFFFD\uFFFDx", streamSanitize(s, "\xf0\x9f\x98x"))
		assert.Equal(t, "\uFFFD\uFFFD\uFFFDx", streamSanitize(s, "\xf0\x9f", "\x98", "x"))
		assert.Equal(t, "\uFFFD\uFFFD\uFFFDok", streamSanitize(s, "\xf0\x9f\x98\x1b[31mok\x1b[0m"))
		assert.Equal(t, "\uFFFD\uFFFD\uFFFDok", streamSanitize(s, "\xf0\x9f\x98", "\x1b[31mok\x1b[0m"))

		assert.Equal(t, "\uFFFD\uFFFDx", SanitizeTerminalText("\xe2\x80x"))
		assert.Equal(t, "\uFFFD\uFFFDok", SanitizeTerminalText("\xe2\x80\x1b[31mok\x1b[0m"))
	})

	t.Run("handles 0xc2 inside OSC and DCS followed by non-0x9c bytes", func(t *testing.T) {
		cases := []struct {
			name   string
			chunks []string
		}{
			{name: "UTF-8 copyright inside OSC", chunks: []string{"\x1b]0;©\x07ok"}},
			{name: "split UTF-8 copyright inside OSC", chunks: []string{"\x1b]0;\xc2", "\xa9\x07ok"}},
			{name: "0xc2 followed by BEL in OSC", chunks: []string{"\x1b]0;\xc2\x07ok"}},
			{name: "split 0xc2 followed by BEL in OSC", chunks: []string{"\x1b]0;\xc2", "\x07ok"}},
			{name: "0xc2 followed by 7-bit ST in OSC", chunks: []string{"\x1b]0;\xc2\x1b\\ok"}},
			{name: "split 0xc2 followed by 7-bit ST in OSC", chunks: []string{"\x1b]0;\xc2", "\x1b\\ok"}},
			{name: "0xc2 followed by 8-bit ST in OSC", chunks: []string{"\x1b]0;\xc2\xc2\x9cok"}},
			{name: "split 0xc2 followed by 8-bit ST in OSC", chunks: []string{"\x1b]0;\xc2", "\xc2", "\x9cok"}},
			{name: "UTF-8 copyright inside DCS", chunks: []string{"\x1bP©\x1b\\ok"}},
			{name: "split 0xc2 followed by 8-bit ST in DCS", chunks: []string{"\x1bP\xc2", "\xc2", "\x9cok"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := NewTerminalSanitizer()
				assert.Equal(t, "ok", streamSanitize(s, tc.chunks...))
				assert.Equal(t, "ok", SanitizeTerminalText(strings.Join(tc.chunks, "")))
			})
		}
	})
}
