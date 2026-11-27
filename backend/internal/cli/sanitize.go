package cli

import (
	"strings"
	"unicode/utf8"
)

type sanitizerState uint8

const (
	sanStateGround sanitizerState = iota
	sanStateC2
	sanStateESC
	sanStateESCIntermediate
	sanStateCSIParams
	sanStateCSIIntermediates
	sanStateString
	sanStateStringESC
	sanStateStringC2
)

// TerminalSanitizer is a single-pass streaming escape and control sanitizer.
// It holds only O(1) parser state plus up to 3 trailing bytes of a split
// UTF-8 rune, so arbitrarily large OSC, DCS, APC, PM, and SOS payloads never
// buffer or leak to output.
type TerminalSanitizer struct {
	state      sanitizerState
	singleLine bool
	pending    []byte
	pendingBuf [4]byte
}

// NewTerminalSanitizer returns a streaming sanitizer that preserves newlines
// and tabs while stripping escape sequences, carriage returns, control
// characters, and Unicode bidirectional (BiDi) overrides.
func NewTerminalSanitizer() *TerminalSanitizer {
	s := &TerminalSanitizer{}
	s.pending = s.pendingBuf[:0]
	return s
}

// NewSingleLineTerminalSanitizer returns a streaming sanitizer that strips
// escape sequences, control characters, and BiDi overrides and replaces
// carriage returns, newlines, and tabs with spaces.
func NewSingleLineTerminalSanitizer() *TerminalSanitizer {
	s := &TerminalSanitizer{singleLine: true}
	s.pending = s.pendingBuf[:0]
	return s
}

// Reset clears parser state and pending UTF-8 bytes while retaining buffer
// capacity and single-line mode.
func (s *TerminalSanitizer) Reset() {
	s.state = sanStateGround
	s.pending = s.pending[:0]
}

// Write consumes one chunk of raw terminal bytes and returns the sanitized
// output that is complete as of this chunk.
func (s *TerminalSanitizer) Write(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(len(chunk))
	for _, b := range chunk {
		s.step(&out, b)
	}
	return out.String()
}

// Flush finalizes any incomplete trailing UTF-8 sequence at EOF and returns
// parser state to ground. Truncated escape sequences are discarded.
func (s *TerminalSanitizer) Flush() string {
	if s.state != sanStateC2 && len(s.pending) == 0 {
		s.state = sanStateGround
		return ""
	}
	var out strings.Builder
	s.flushTo(&out)
	return out.String()
}

func (s *TerminalSanitizer) flushTo(out *strings.Builder) {
	if s.state == sanStateC2 {
		out.WriteRune(utf8.RuneError)
	}
	s.drainPendingUTF8(out)
	s.state = sanStateGround
}

func (s *TerminalSanitizer) step(out *strings.Builder, b byte) {
reprocess:
	switch s.state {
	case sanStateGround:
		if len(s.pending) > 0 {
			if b >= 0x80 && b <= 0xbf {
				s.pending = append(s.pending, b)
				for len(s.pending) > 0 && utf8.FullRune(s.pending) {
					s.emitPendingRune(out)
				}
				return
			}
			s.drainPendingUTF8(out)
		}
		if b == 0x1b {
			s.state = sanStateESC
			return
		}
		if b == 0xc2 {
			s.state = sanStateC2
			return
		}
		if b < 0x80 {
			if b >= 0x20 && b < 0x7f {
				out.WriteByte(b)
			} else if b == '\n' || b == '\r' || b == '\t' {
				if s.singleLine {
					out.WriteByte(' ')
				} else if b != '\r' {
					out.WriteByte(b)
				}
			}
			return
		}
		if b < 0xc2 || b > 0xf4 {
			out.WriteRune(utf8.RuneError)
			return
		}
		s.pending = append(s.pending, b)

	case sanStateC2:
		switch {
		case b == 0x9b: // 8-bit C1 CSI
			s.state = sanStateCSIParams
		case b == 0x90 || b == 0x98 || b == 0x9d || b == 0x9e || b == 0x9f: // 8-bit C1 DCS/SOS/OSC/PM/APC
			s.state = sanStateString
		case b >= 0x80 && b <= 0x9f: // Other 8-bit C1 control
			s.state = sanStateGround
		case b >= 0xa0 && b <= 0xbf: // Valid 2-byte UTF-8 rune U+00A0..U+00BF
			s.state = sanStateGround
			if r := rune(b); !isUnsafeTerminalRune(r) {
				out.WriteRune(r)
			}
		default:
			out.WriteRune(utf8.RuneError)
			s.state = sanStateGround
			goto reprocess
		}

	case sanStateESC:
		switch {
		case b == '[':
			s.state = sanStateCSIParams
		case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X':
			s.state = sanStateString
		case b >= 0x20 && b <= 0x2f: // ECMA-48 intermediate bytes (includes charset and ESC # / ESC %)
			s.state = sanStateESCIntermediate
		case b >= 0x30 && b <= 0x7e: // 2-byte Fe/Fp/Fs final byte
			s.state = sanStateGround
		case b == 0x1b:
			// Stay in sanStateESC for the new ESC.
		default:
			s.state = sanStateGround
			goto reprocess
		}

	case sanStateESCIntermediate:
		switch {
		case b >= 0x20 && b <= 0x2f:
			// Additional ECMA-48 intermediate byte.
		case b >= 0x30 && b <= 0x7e:
			s.state = sanStateGround
		case b == 0x1b:
			s.state = sanStateESC
		default:
			s.state = sanStateGround
			goto reprocess
		}

	case sanStateCSIParams:
		switch {
		case b >= 0x30 && b <= 0x3f:
			// Parameter byte.
		case b >= 0x20 && b <= 0x2f:
			s.state = sanStateCSIIntermediates
		case b >= 0x40 && b <= 0x7e:
			s.state = sanStateGround
		case b == 0x1b:
			s.state = sanStateESC
		default:
			s.state = sanStateGround
			goto reprocess
		}

	case sanStateCSIIntermediates:
		switch {
		case b >= 0x20 && b <= 0x2f:
			// Intermediate byte.
		case b >= 0x40 && b <= 0x7e:
			s.state = sanStateGround
		case b == 0x1b:
			s.state = sanStateESC
		default:
			s.state = sanStateGround
			goto reprocess
		}

	case sanStateString:
		switch b {
		case 0x07: // BEL terminates OSC/string sequences
			s.state = sanStateGround
		case 0x1b:
			s.state = sanStateStringESC
		case 0xc2:
			s.state = sanStateStringC2
		}

	case sanStateStringESC:
		switch b {
		case '\\': // 7-bit ST (ESC \)
			s.state = sanStateGround
		case 0x07:
			s.state = sanStateGround
		case 0x1b:
			// Stay in sanStateStringESC so ESC ESC \ still terminates.
		case 0xc2:
			s.state = sanStateStringC2
		default:
			// Inner ESC <non-\> stays inside the string sequence.
			s.state = sanStateString
		}

	case sanStateStringC2:
		switch b {
		case 0x9c: // 8-bit ST (U+009C)
			s.state = sanStateGround
		case 0x07:
			s.state = sanStateGround
		case 0x1b:
			s.state = sanStateStringESC
		case 0xc2:
			// Stay in sanStateStringC2.
		default:
			s.state = sanStateString
		}
	}
}

func (s *TerminalSanitizer) emitPendingRune(out *strings.Builder) {
	r, size := utf8.DecodeRune(s.pending)
	if r == utf8.RuneError && size == 1 {
		out.WriteRune(utf8.RuneError)
	} else if !isUnsafeTerminalRune(r) {
		out.WriteRune(r)
	}
	copy(s.pending, s.pending[size:])
	s.pending = s.pending[:len(s.pending)-size]
}

func (s *TerminalSanitizer) drainPendingUTF8(out *strings.Builder) {
	for len(s.pending) > 0 {
		s.emitPendingRune(out)
	}
}

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
	if isCleanASCII(s, singleLine) {
		return s
	}
	var ts TerminalSanitizer
	ts.singleLine = singleLine
	ts.pending = ts.pendingBuf[:0]
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ts.step(&out, s[i])
	}
	ts.flushTo(&out)
	return out.String()
}

func isCleanASCII(s string, singleLine bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c < 0x7f {
			continue
		}
		if !singleLine && (c == '\n' || c == '\t') {
			continue
		}
		return false
	}
	return true
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
