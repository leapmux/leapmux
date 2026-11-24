package cli

import (
	"strings"
	"unicode/utf8"
)

// SanitizeTerminalText removes terminal escape sequences, control characters,
// and Unicode bidirectional (BiDi) overrides from untrusted text before it is
// written to a human-readable terminal stream. Newlines and tabs are preserved
// so multi-line prose stays readable; use SanitizeSingleLine for table cells
// and one-line summaries.
func SanitizeTerminalText(s string) string {
	return sanitizeTerminal(s, false)
}

// SanitizeSingleLine strips escape sequences, control characters, and BiDi
// overrides and collapses newlines and tabs into spaces.
func SanitizeSingleLine(s string) string {
	return sanitizeTerminal(s, true)
}

func sanitizeTerminal(s string, singleLine bool) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscapeSequence(s, i)
			continue
		}
		// 8-bit C1 CSI (U+009B, UTF-8 0xC2 0x9B), string sequences (DCS/SOS/OSC/PM/APC),
		// or other C1 controls (U+0080..U+009F).
		if s[i] == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f {
			switch s[i+1] {
			case 0x9b:
				i = skipCSIBody(s, i+2)
				continue
			case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
				i = skipStringSequenceBody(s, i+2)
				continue
			default:
				i += 2
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == '\n' || r == '\r' || r == '\t' {
			if singleLine {
				b.WriteByte(' ')
			} else if r != '\r' {
				b.WriteRune(r)
			}
			continue
		}
		if isUnsafeTerminalRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// skipEscapeSequence advances past one ESC-initiated ANSI/VT sequence starting at i.
func skipEscapeSequence(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	next := s[i+1]
	switch next {
	case '[': // CSI: ESC [ params intermediates final(0x40..0x7E)
		return skipCSIBody(s, i+2)
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: terminated by BEL, ESC \, or 8-bit ST
		return skipStringSequenceBody(s, i+2)
	case '(', ')', '*', '+', '-', '.', '/': // Charset designation: 3-byte sequence
		if i+2 < len(s) {
			return i + 3
		}
		return len(s)
	default:
		// 2-byte Fe/Fp/Fs sequence.
		return i + 2
	}
}

func skipStringSequenceBody(s string, j int) int {
	for j < len(s) {
		if s[j] == 0x07 {
			return j + 1
		}
		if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
			return j + 2
		}
		if s[j] == 0xc2 && j+1 < len(s) && s[j+1] == 0x9c {
			return j + 2
		}
		j++
	}
	return j
}

func skipCSIBody(s string, j int) int {
	for j < len(s) {
		c := s[j]
		j++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return j
}

func isUnsafeTerminalRune(r rune) bool {
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
		return true
	}
	switch r {
	case '\u061c', // Arabic letter mark
		'\u200e', '\u200f', // LRM / RLM
		'\u2028', '\u2029', // Line / paragraph separator
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e', // BiDi embedding / override
		'\u2066', '\u2067', '\u2068', '\u2069': // BiDi isolates
		return true
	}
	return false
}
