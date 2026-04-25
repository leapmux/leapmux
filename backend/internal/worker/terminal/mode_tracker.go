package terminal

// modeTracker observes PTY output bytes and keeps a minimal model of
// sticky xterm state. It is NOT a terminal emulator — it tracks only
// the modes listed below. Unknown escape sequences are skipped without
// disturbing tracked state.
//
// Tracked modes (each yields a fragment of snapshotPrefix when set):
//
//   - Alt screen — DEC private modes 47, 1047, 1049 (smcup/rmcup),
//     emitted as 1049.
//   - Cursor visibility — DEC private mode 25 (DECTCEM).
//   - Autowrap — DEC private mode 7 (DECAWM); default is ON, only
//     emitted when a program disabled it.
//   - Application cursor keys — DEC private mode 1 (DECCKM).
//   - Bracketed paste — DEC private mode 2004.
//   - Mouse tracking — DEC private modes 1000, 1002, 1003 (one slot,
//     last-write-wins).
//   - Mouse encoding — DEC private modes 1006, 1015 (independent slot,
//     last-write-wins).
//   - Window title — OSC 0/2 last-write-wins string, capped at oscBufCap.
//
// All methods must be called under the enclosing ScreenBuffer's mutex.
type modeTracker struct {
	altScreen      bool
	cursorHidden   bool
	autoWrapOff    bool // default ON: store negation so zero-value matches xterm.
	appCursorKeys  bool
	bracketedPaste bool
	mouseTrack     mouseTrackMode
	mouseEncoding  mouseEncodingMode
	title          []byte // last OSC 0/2 title; nil when never set.

	parseState parseState
	paramBuf   [paramBufCap]byte
	paramLen   int
	oscBuf     [oscBufCap]byte
	oscLen     int
	oscOverrun bool // true once oscLen hit cap; drop the OSC, keep parsing terminator.
}

type parseState uint8

const (
	stateGround parseState = iota
	stateEsc
	stateCSI
	stateOSC
	stateOSCEsc
)

type mouseTrackMode uint8

const (
	mouseTrackOff mouseTrackMode = iota
	mouseTrackX10
	mouseTrackBtnEvent
	mouseTrackAnyEvent
)

type mouseEncodingMode uint8

const (
	mouseEncOff mouseEncodingMode = iota
	mouseEncSGR
	mouseEncURXVT
)

const (
	paramBufCap = 64  // CSI parameter cap; overflow aborts the sequence.
	oscBufCap   = 256 // OSC body cap; overflow drops the OSC body silently.
)

// feed processes a chunk of PTY output. Allocation-free when the chunk
// contains no escape sequences (the >99% case for shell output). Partial
// sequences at the tail are buffered in parseState/paramBuf/oscBuf and
// completed on the next call.
func (t *modeTracker) feed(data []byte) {
	for _, b := range data {
		switch t.parseState {
		case stateGround:
			if b == 0x1b {
				t.parseState = stateEsc
			}
		case stateEsc:
			t.handleEscIntro(b)
		case stateCSI:
			// `\x1b` mid-CSI starts a fresh escape: abort the current
			// sequence (paramBuf is wiped on the next stateCSI entry).
			if b == 0x1b {
				t.parseState = stateEsc
				continue
			}
			if b >= 0x40 && b <= 0x7e {
				t.dispatchCSI(b)
				t.parseState = stateGround
				continue
			}
			if t.paramLen >= paramBufCap {
				// Overflow: abort. Drop bytes until a final byte (or a
				// fresh `\x1b`, handled above) ends the sequence. The
				// final byte itself must not dispatch — track via
				// paramLen sentinel + skipping the dispatch branch.
				t.parseState = stateGround
				continue
			}
			t.paramBuf[t.paramLen] = b
			t.paramLen++
		case stateOSC:
			switch b {
			case 0x07: // BEL terminator.
				t.dispatchOSC()
				t.parseState = stateGround
			case 0x1b:
				t.parseState = stateOSCEsc
			default:
				if t.oscLen < oscBufCap {
					t.oscBuf[t.oscLen] = b
					t.oscLen++
				} else {
					t.oscOverrun = true
				}
			}
		case stateOSCEsc:
			if b == '\\' {
				// ST terminator (\x1b\\) — finalize the OSC.
				t.dispatchOSC()
				t.parseState = stateGround
				continue
			}
			// The `\x1b` mid-OSC was the start of a new escape, not ST.
			// Abandon the OSC body and re-enter stateEsc handling with
			// this byte as the escape's second byte.
			t.oscLen = 0
			t.oscOverrun = false
			t.handleEscIntro(b)
		}
	}
}

// handleEscIntro processes the second byte of an escape sequence (the
// byte after `\x1b`). Shared by stateEsc and the OSC-aborted recovery
// path so a `\x1b[?1049h` immediately following an unterminated OSC
// still parses.
func (t *modeTracker) handleEscIntro(b byte) {
	switch b {
	case '[':
		t.paramLen = 0
		t.parseState = stateCSI
	case ']':
		t.oscLen = 0
		t.oscOverrun = false
		t.parseState = stateOSC
	case 0x1b:
		// Back-to-back ESC: stay in stateEsc so the next byte is read
		// as a fresh intro.
		t.parseState = stateEsc
	default:
		// Charset designators (`(`, `)`), save/restore cursor (`7`/`8`),
		// single-byte escapes — all out of scope.
		t.parseState = stateGround
	}
}

// dispatchCSI handles a complete CSI sequence whose final byte is `final`.
// The parameter buffer is `t.paramBuf[:t.paramLen]`. We only act on `h`
// (set) and `l` (reset) of DEC private modes (params start with `?`).
func (t *modeTracker) dispatchCSI(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	params := t.paramBuf[:t.paramLen]
	if len(params) == 0 || params[0] != '?' {
		return
	}
	set := final == 'h'
	// Walk `;`-separated decimal numbers after the leading `?`. The
	// loop bound is `i <= len(params)` so the iteration past the end
	// flushes the trailing number — a real param has no `;` after it.
	n := 0
	hasDigit := false
	for i := 1; i <= len(params); i++ {
		if i == len(params) || params[i] == ';' {
			if hasDigit {
				t.applyMode(n, set)
			}
			n = 0
			hasDigit = false
			continue
		}
		c := params[i]
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			hasDigit = true
		} else {
			// Non-digit, non-`;` inside DEC params (e.g. another `?`).
			// Treat as separator: flush current and reset.
			if hasDigit {
				t.applyMode(n, set)
			}
			n = 0
			hasDigit = false
		}
	}
}

func (t *modeTracker) applyMode(code int, set bool) {
	switch code {
	case 1:
		t.appCursorKeys = set
	case 7:
		t.autoWrapOff = !set
	case 25:
		t.cursorHidden = !set
	case 47, 1047, 1049:
		t.altScreen = set
	case 2004:
		t.bracketedPaste = set
	case 1000:
		if set {
			t.mouseTrack = mouseTrackX10
		} else if t.mouseTrack == mouseTrackX10 {
			t.mouseTrack = mouseTrackOff
		}
	case 1002:
		if set {
			t.mouseTrack = mouseTrackBtnEvent
		} else if t.mouseTrack == mouseTrackBtnEvent {
			t.mouseTrack = mouseTrackOff
		}
	case 1003:
		if set {
			t.mouseTrack = mouseTrackAnyEvent
		} else if t.mouseTrack == mouseTrackAnyEvent {
			t.mouseTrack = mouseTrackOff
		}
	case 1006:
		if set {
			t.mouseEncoding = mouseEncSGR
		} else if t.mouseEncoding == mouseEncSGR {
			t.mouseEncoding = mouseEncOff
		}
	case 1015:
		if set {
			t.mouseEncoding = mouseEncURXVT
		} else if t.mouseEncoding == mouseEncURXVT {
			t.mouseEncoding = mouseEncOff
		}
	}
}

// dispatchOSC handles a complete OSC body sitting in t.oscBuf[:t.oscLen].
// We only care about Ps == 0 or 2 (window title). Bodies that overflowed
// the cap are dropped (oscOverrun==true).
func (t *modeTracker) dispatchOSC() {
	defer func() {
		t.oscLen = 0
		t.oscOverrun = false
	}()
	if t.oscOverrun {
		return
	}
	body := t.oscBuf[:t.oscLen]
	// Body shape: `<Ps>;<text>`. We accept "0" or "2" as Ps.
	if len(body) < 2 || body[1] != ';' {
		return
	}
	if body[0] != '0' && body[0] != '2' {
		return
	}
	text := body[2:]
	// Clone — we're aliasing a fixed-size array.
	t.title = append(t.title[:0], text...)
}

// snapshotPrefix returns the escape sequences that reproduce the
// tracker's current state when prepended to a byte replay starting from
// a fresh xterm. Returns nil when every mode is at its default (the
// caller can skip an extra append). Always returns a freshly allocated
// slice; never aliases internal state.
func (t *modeTracker) snapshotPrefix() []byte {
	// Worst-case length budget: each mode emission is short (≤ ~10
	// bytes) and there are ~7 of them, plus the title (≤ oscBufCap+5).
	// Pre-size on the high end to avoid reallocations; trim by returning
	// the slice as-is.
	if t.isDefault() {
		return nil
	}
	out := make([]byte, 0, 64+len(t.title))

	if t.altScreen {
		out = append(out, "\x1b[?1049h"...)
	}
	if t.cursorHidden {
		out = append(out, "\x1b[?25l"...)
	}
	if t.autoWrapOff {
		out = append(out, "\x1b[?7l"...)
	}
	if t.appCursorKeys {
		out = append(out, "\x1b[?1h"...)
	}
	if t.bracketedPaste {
		out = append(out, "\x1b[?2004h"...)
	}
	switch t.mouseTrack {
	case mouseTrackX10:
		out = append(out, "\x1b[?1000h"...)
	case mouseTrackBtnEvent:
		out = append(out, "\x1b[?1002h"...)
	case mouseTrackAnyEvent:
		out = append(out, "\x1b[?1003h"...)
	}
	switch t.mouseEncoding {
	case mouseEncSGR:
		out = append(out, "\x1b[?1006h"...)
	case mouseEncURXVT:
		out = append(out, "\x1b[?1015h"...)
	}
	if t.title != nil {
		out = append(out, "\x1b]0;"...)
		out = append(out, t.title...)
		out = append(out, 0x07)
	}
	return out
}

// isDefault reports whether every tracked field equals its zero/default
// value. Used to short-circuit snapshotPrefix to a nil return.
func (t *modeTracker) isDefault() bool {
	return !t.altScreen &&
		!t.cursorHidden &&
		!t.autoWrapOff &&
		!t.appCursorKeys &&
		!t.bracketedPaste &&
		t.mouseTrack == mouseTrackOff &&
		t.mouseEncoding == mouseEncOff &&
		t.title == nil
}
