package terminal

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"strconv"
	"strings"
)

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

	// Per-chunk signal coalescing (reset by beginChunk, drained by drainSignals).
	chunkSignals    []Signal
	chunkBell       bool
	chunkNotifCount int
	droppedNotifs   int

	kittyPending map[string]*kittyPending
	kittyDropped map[string]struct{}
}

type kittyPending struct {
	title string
	body  string
	bytes int
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
	paramBufCap              = 64   // CSI parameter cap; overflow aborts the sequence.
	oscBufCap                = 2048 // OSC body cap; overflow drops the OSC body silently.
	maxNotificationsPerChunk = 8
	kittyMaxPending          = 4
	kittyMaxBytes            = 4096
	// kittyMaxDropped bounds the banned-id set. Without it a long-lived
	// terminal emitting many distinct oversized (>kittyMaxBytes) kitty
	// notifications grows kittyDropped without limit (the set is insert-only
	// and lives for the modeTracker's lifetime). At the cap we clear and start
	// over rather than refusing to ban, so a fresh burst of oversized
	// notifications is still throttled.
	kittyMaxDropped = 64
)

// beginChunk resets per-Write signal coalescing state. Call once before feed
// for each ScreenBuffer.Write.
func (t *modeTracker) beginChunk() {
	t.chunkSignals = t.chunkSignals[:0]
	t.chunkBell = false
	t.chunkNotifCount = 0
	t.droppedNotifs = 0
}

// drainSignals returns signals observed during the current chunk and clears
// the coalescing slot.
func (t *modeTracker) drainSignals() []Signal {
	if t.droppedNotifs > 0 {
		slog.Warn("terminal OSC notifications dropped in one PTY chunk",
			"dropped", t.droppedNotifs,
			"limit", maxNotificationsPerChunk,
		)
	}
	out := append([]Signal(nil), t.chunkSignals...)
	t.chunkSignals = t.chunkSignals[:0]
	t.chunkBell = false
	t.chunkNotifCount = 0
	t.droppedNotifs = 0
	return out
}

// feed processes a chunk of PTY output. Allocation-free when the chunk
// contains no escape sequences (the >99% case for shell output). Partial
// sequences at the tail are buffered in parseState/paramBuf/oscBuf and
// completed on the next call.
func (t *modeTracker) feed(data []byte) {
	for _, b := range data {
		switch t.parseState {
		case stateGround:
			switch b {
			case 0x1b:
				t.parseState = stateEsc
			case 0x07:
				t.emitBell()
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
				// Param buffer full — bail to ground state. Subsequent
				// bytes (including the would-be final byte that ends
				// this CSI) are read as plain text and ignored until a
				// fresh `\x1b` starts a new escape. Dropping the entire
				// sequence is the safe choice: a truncated DEC param
				// list cannot be partially applied without risking
				// silently flipping the wrong mode.
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
		setOrClearSlot(&t.mouseTrack, mouseTrackX10, mouseTrackOff, set)
	case 1002:
		setOrClearSlot(&t.mouseTrack, mouseTrackBtnEvent, mouseTrackOff, set)
	case 1003:
		setOrClearSlot(&t.mouseTrack, mouseTrackAnyEvent, mouseTrackOff, set)
	case 1006:
		setOrClearSlot(&t.mouseEncoding, mouseEncSGR, mouseEncOff, set)
	case 1015:
		setOrClearSlot(&t.mouseEncoding, mouseEncURXVT, mouseEncOff, set)
	}
}

// setOrClearSlot updates a single-slot mode (where multiple DEC codes
// compete for the same field, last-write-wins): on `set==true` the slot
// becomes `on`; on `set==false` it falls back to `off` only if the slot
// was holding `on` — so resetting an inactive variant is a no-op and
// can't accidentally clear a different variant of the same family.
func setOrClearSlot[T comparable](slot *T, on, off T, set bool) {
	switch {
	case set:
		*slot = on
	case *slot == on:
		*slot = off
	}
}

// dispatchOSC handles a complete OSC body sitting in t.oscBuf[:t.oscLen].
// Bodies that overflowed the cap are dropped (oscOverrun==true).
func (t *modeTracker) dispatchOSC() {
	defer func() {
		t.oscLen = 0
		t.oscOverrun = false
	}()
	if t.oscOverrun {
		return
	}
	body := t.oscBuf[:t.oscLen]
	if len(body) == 0 {
		return
	}
	semi := bytes.IndexByte(body, ';')
	if semi < 0 {
		return
	}
	ps := string(body[:semi])
	rest := body[semi+1:]

	switch ps {
	case "0", "2":
		t.setTitle(rest)
	case "1":
		// Icon name only — ignored for title signals and snapshot title slot.
	case "9":
		t.dispatchOSC9(rest)
	case "777":
		t.dispatchOSC777(rest)
	case "99":
		t.dispatchOSC99(rest)
	}
}

func (t *modeTracker) setTitle(text []byte) {
	if t.title != nil && bytes.Equal(t.title, text) {
		return
	}
	t.title = append(t.title[:0], text...)
	t.replaceLastSignal(SignalTitle, Signal{Kind: SignalTitle, Title: string(text)})
}

func (t *modeTracker) dispatchOSC9(rest []byte) {
	if bytes.HasPrefix(rest, []byte("4;")) {
		t.dispatchOSC9Progress(rest[2:])
		return
	}
	t.emitNotification("", string(rest))
}

func (t *modeTracker) dispatchOSC9Progress(rest []byte) {
	parts := bytes.SplitN(rest, []byte(";"), 2)
	if len(parts) == 0 || len(parts[0]) == 0 {
		return
	}
	stateVal, err := strconv.Atoi(string(parts[0]))
	if err != nil {
		return
	}
	var state ProgressState
	switch stateVal {
	case 0:
		state = ProgressClear
	case 1:
		state = ProgressNormal
	case 2:
		state = ProgressError
	case 3:
		state = ProgressIndeterminate
	case 4:
		state = ProgressPaused
	default:
		return
	}
	var percent int32
	if len(parts) == 2 && len(parts[1]) > 0 {
		p, err := strconv.Atoi(string(parts[1]))
		if err != nil {
			return
		}
		// Clamp to the documented 0..100 range rather than narrowing an
		// arbitrary `int` to int32 (which silently wraps for out-of-range
		// values like `OSC 9;4;1;9999999999`, producing a bogus/negative
		// percent on the wire).
		if p < 0 {
			p = 0
		} else if p > 100 {
			p = 100
		}
		percent = int32(p)
	}
	t.replaceLastSignal(SignalProgress, Signal{Kind: SignalProgress, State: state, Percent: percent})
}

func (t *modeTracker) dispatchOSC777(rest []byte) {
	parts := strings.SplitN(string(rest), ";", 3)
	if len(parts) < 3 || parts[0] != "notify" {
		return
	}
	t.emitNotification(parts[1], parts[2])
}

func (t *modeTracker) dispatchOSC99(rest []byte) {
	semi := bytes.IndexByte(rest, ';')
	if semi < 0 {
		return
	}
	meta := rest[:semi]
	payload := rest[semi+1:]

	id := ""
	done := true
	part := "title"
	encoded := false
	for _, field := range bytes.Split(meta, []byte(":")) {
		kv := bytes.SplitN(field, []byte("="), 2)
		if len(kv) != 2 {
			continue
		}
		switch string(kv[0]) {
		case "i":
			id = string(kv[1])
		case "d":
			done = string(kv[1]) != "0"
		case "p":
			part = string(kv[1])
		case "e":
			encoded = string(kv[1]) == "1"
		}
	}
	if part == "?" {
		return
	}
	if done && t.kittyDropped != nil {
		if _, banned := t.kittyDropped[id]; banned {
			return
		}
	}

	text := string(payload)
	if encoded {
		decoded, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return
		}
		text = string(decoded)
	}

	if !done {
		p := t.kittyEnsure(id)
		if p == nil {
			return
		}
		add := len(text)
		if p.bytes+add > kittyMaxBytes {
			t.kittyDrop(id)
			t.kittyMarkDropped(id)
			return
		}
		switch part {
		case "body":
			p.body += text
		default:
			p.title += text
		}
		p.bytes += add
		return
	}

	title, body := text, ""
	if part == "body" {
		title, body = "", text
	}
	if pending := t.kittyPending[id]; pending != nil {
		add := len(text)
		if pending.bytes+add > kittyMaxBytes {
			t.kittyDrop(id)
			t.kittyMarkDropped(id)
			return
		}
		if pending.title != "" {
			title = pending.title + title
		}
		if pending.body != "" {
			body = pending.body + body
		}
		t.kittyDrop(id)
	}
	t.emitNotification(title, body)
}

func (t *modeTracker) kittyEnsure(id string) *kittyPending {
	if t.kittyDropped != nil {
		if _, banned := t.kittyDropped[id]; banned {
			return nil
		}
	}
	if p, ok := t.kittyPending[id]; ok {
		return p
	}
	if t.kittyPending == nil {
		t.kittyPending = make(map[string]*kittyPending)
	}
	if len(t.kittyPending) >= kittyMaxPending {
		return nil
	}
	p := &kittyPending{}
	t.kittyPending[id] = p
	return p
}

func (t *modeTracker) kittyDrop(id string) {
	if t.kittyPending == nil {
		return
	}
	delete(t.kittyPending, id)
}

// kittyMarkDropped bans id from future notification delivery (an oversized
// payload cannot be reassembled). The dropped set is bounded so a terminal
// emitting many distinct oversized notifications cannot grow it without limit.
func (t *modeTracker) kittyMarkDropped(id string) {
	if t.kittyDropped == nil {
		t.kittyDropped = make(map[string]struct{})
	} else if len(t.kittyDropped) >= kittyMaxDropped {
		// At capacity, clear and start over rather than refusing to ban: a
		// fresh burst of oversized notifications is still throttled, and old
		// (likely stale) bans are the safest to forget.
		t.kittyDropped = make(map[string]struct{})
	}
	t.kittyDropped[id] = struct{}{}
}

func (t *modeTracker) emitBell() {
	if t.chunkBell {
		return
	}
	t.chunkBell = true
	t.chunkSignals = append(t.chunkSignals, Signal{Kind: SignalBell})
}

func (t *modeTracker) emitNotification(title, body string) {
	if t.chunkNotifCount >= maxNotificationsPerChunk {
		t.droppedNotifs++
		return
	}
	t.chunkNotifCount++
	t.chunkSignals = append(t.chunkSignals, Signal{
		Kind:  SignalNotification,
		Title: title,
		Body:  body,
	})
}

func (t *modeTracker) replaceLastSignal(kind SignalKind, sig Signal) {
	for i := len(t.chunkSignals) - 1; i >= 0; i-- {
		if t.chunkSignals[i].Kind == kind {
			t.chunkSignals[i] = sig
			return
		}
	}
	t.chunkSignals = append(t.chunkSignals, sig)
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
	case mouseTrackOff:
		// Off is the terminal default, so there is no DECSET to replay.
	case mouseTrackX10:
		out = append(out, "\x1b[?1000h"...)
	case mouseTrackBtnEvent:
		out = append(out, "\x1b[?1002h"...)
	case mouseTrackAnyEvent:
		out = append(out, "\x1b[?1003h"...)
	}
	switch t.mouseEncoding {
	case mouseEncOff:
		// Default encoding needs no DECSET; X10-style reports are implied.
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
