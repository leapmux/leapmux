package terminal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feedString is a small helper so tests can write expressive strings
// rather than `[]byte("\x1b[?1049h")` repeatedly.
func feedString(t *modeTracker, s string) {
	t.feed([]byte(s))
}

// TestModeTracker_PerModeSetReset is the table that covers every tracked
// DEC private mode end-to-end: the canonical set sequence flips the
// field, the canonical reset sequence flips it back, and snapshotPrefix
// reflects the current state.
func TestModeTracker_PerModeSetReset(t *testing.T) {
	cases := []struct {
		name           string
		setSeq         string
		resetSeq       string
		expectInPrefix string
	}{
		{"alt screen 1049", "\x1b[?1049h", "\x1b[?1049l", "\x1b[?1049h"},
		{"cursor visibility", "\x1b[?25l", "\x1b[?25h", "\x1b[?25l"},
		{"autowrap off", "\x1b[?7l", "\x1b[?7h", "\x1b[?7l"},
		{"app cursor keys", "\x1b[?1h", "\x1b[?1l", "\x1b[?1h"},
		{"bracketed paste", "\x1b[?2004h", "\x1b[?2004l", "\x1b[?2004h"},
		{"mouse track 1000", "\x1b[?1000h", "\x1b[?1000l", "\x1b[?1000h"},
		{"mouse track 1002", "\x1b[?1002h", "\x1b[?1002l", "\x1b[?1002h"},
		{"mouse track 1003", "\x1b[?1003h", "\x1b[?1003l", "\x1b[?1003h"},
		{"mouse encoding 1006", "\x1b[?1006h", "\x1b[?1006l", "\x1b[?1006h"},
		{"mouse encoding 1015", "\x1b[?1015h", "\x1b[?1015l", "\x1b[?1015h"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/set then reset → empty prefix", func(t *testing.T) {
			tr := &modeTracker{}
			feedString(tr, tc.setSeq)
			assert.Contains(t, string(tr.snapshotPrefix()), tc.expectInPrefix,
				"set sequence must produce the corresponding prefix bytes")
			feedString(tr, tc.resetSeq)
			assert.Nil(t, tr.snapshotPrefix(),
				"reset sequence must return prefix to nil")
		})
	}
}

// TestModeTracker_AltScreenAliases covers the legacy private modes 47
// and 1047 — semantically equivalent to 1049 for our purposes. Programs
// that predate 1049 (some tmux configs, older curses) emit the older
// codes; emission must always normalize to 1049 so xterm gets the most
// complete restore (1049 == save cursor + alt screen).
func TestModeTracker_AltScreenAliases(t *testing.T) {
	for _, seq := range []string{"\x1b[?47h", "\x1b[?1047h", "\x1b[?1049h"} {
		tr := &modeTracker{}
		feedString(tr, seq)
		assert.Equal(t, []byte("\x1b[?1049h"), tr.snapshotPrefix(),
			"%q must drive altScreen and emit 1049 on snapshot", seq)
	}
}

// TestModeTracker_MultiParamCSI: a single CSI with several `;`-separated
// parameters must update each field independently. xterm accepts this
// form even though most programs split into separate sequences.
func TestModeTracker_MultiParamCSI(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[?25;7l") // hide cursor + autowrap off in one go.
	prefix := string(tr.snapshotPrefix())
	assert.Contains(t, prefix, "\x1b[?25l")
	assert.Contains(t, prefix, "\x1b[?7l")
}

// TestModeTracker_PartialAcrossFeeds: a sequence chopped at every byte
// boundary must produce the same end-state as the unsplit feed. This is
// the invariant that makes the tracker safe to call from inside Write —
// PTY chunks split at arbitrary boundaries.
func TestModeTracker_PartialAcrossFeeds(t *testing.T) {
	full := "\x1b[?1049h"
	for split := 1; split < len(full); split++ {
		tr := &modeTracker{}
		feedString(tr, full[:split])
		feedString(tr, full[split:])
		assert.Equal(t, []byte("\x1b[?1049h"), tr.snapshotPrefix(),
			"split at %d should still set altScreen", split)
	}
}

// TestModeTracker_UnknownFinalByte: SGR (`m`), erase (`2J`), status
// query (`5n`) and other CSIs that aren't `h`/`l` must leave state
// untouched. This is the cheap-out that keeps SGR explicitly out of
// scope.
func TestModeTracker_UnknownFinalByte(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[31m\x1b[2J\x1b[5n\x1b[H")
	assert.Nil(t, tr.snapshotPrefix(),
		"non-h/l CSIs and non-DEC params must not change tracker state")
}

// TestModeTracker_MixedPrintableAndCSI: printable text bracketing a CSI
// must not pollute state. Verifies that ground-state bytes are truly
// no-ops, not accidentally mutating the tracker.
func TestModeTracker_MixedPrintableAndCSI(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "hello\x1b[?25lworld")
	assert.Equal(t, []byte("\x1b[?25l"), tr.snapshotPrefix())
}

// TestModeTracker_SetThenResetReturnsDefault is the second-most-important
// correctness property after "set survives ring rotation". Programs
// frequently enter alt screen, do work, then exit alt screen before
// returning control to the shell. After that, snapshotPrefix MUST be
// nil so we don't strand reconnecting clients in alt screen.
func TestModeTracker_SetThenResetReturnsDefault(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[?1049h\x1b[?25l\x1b[?2004h")
	require.NotNil(t, tr.snapshotPrefix())
	feedString(tr, "\x1b[?1049l\x1b[?25h\x1b[?2004l")
	assert.Nil(t, tr.snapshotPrefix(),
		"every set sequence reversed → prefix must be nil")
}

// TestModeTracker_FreshIsDefault: the zero value of modeTracker matches
// xterm's default state. No allocation, no prefix, no surprises.
func TestModeTracker_FreshIsDefault(t *testing.T) {
	tr := &modeTracker{}
	assert.Nil(t, tr.snapshotPrefix())
}

// TestModeTracker_EmissionOrdering verifies the documented prefix
// ordering: alt-screen first (so subsequent mode changes land on the
// right buffer), then cursor, autowrap, app-cursor-keys, bracketed
// paste, mouse track, mouse encoding, then title.
func TestModeTracker_EmissionOrdering(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[?2004h\x1b[?25l\x1b[?1049h\x1b[?1006h\x1b[?1002h\x1b[?7l\x1b[?1h\x1b]0;hello\x07")
	prefix := string(tr.snapshotPrefix())
	indices := []int{
		strings.Index(prefix, "\x1b[?1049h"),
		strings.Index(prefix, "\x1b[?25l"),
		strings.Index(prefix, "\x1b[?7l"),
		strings.Index(prefix, "\x1b[?1h"),
		strings.Index(prefix, "\x1b[?2004h"),
		strings.Index(prefix, "\x1b[?1002h"),
		strings.Index(prefix, "\x1b[?1006h"),
		strings.Index(prefix, "\x1b]0;hello\x07"),
	}
	for i, idx := range indices {
		require.NotEqual(t, -1, idx, "fragment %d must be present", i)
	}
	for i := 1; i < len(indices); i++ {
		assert.Less(t, indices[i-1], indices[i],
			"fragments must appear in documented order; broken at index %d", i)
	}
}

// TestModeTracker_ParamBufOverflow: a malicious or malformed CSI with a
// huge parameter run must NOT corrupt state, must NOT allocate without
// bound, and must leave the parser in a recoverable state for the next
// real sequence.
func TestModeTracker_ParamBufOverflow(t *testing.T) {
	tr := &modeTracker{}
	// 200 bytes of "1;" is way past the 64-byte cap.
	overflow := "\x1b[" + strings.Repeat("1;", 200) + "h"
	feedString(tr, overflow)
	assert.Nil(t, tr.snapshotPrefix(),
		"overflowed CSI must not change any tracked field")

	// And the parser must recover for the next valid sequence.
	feedString(tr, "\x1b[?1049h")
	assert.Equal(t, []byte("\x1b[?1049h"), tr.snapshotPrefix())
}

// TestModeTracker_MouseEncodingOrthogonal: 1006/1015 (encoding) and
// 1000/1002/1003 (tracking) live in independent slots. Resetting one
// must not reset the other. Real programs (e.g. neovim) toggle these
// separately on focus events.
func TestModeTracker_MouseEncodingOrthogonal(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[?1006h\x1b[?1002h")
	prefix := string(tr.snapshotPrefix())
	assert.Contains(t, prefix, "\x1b[?1002h")
	assert.Contains(t, prefix, "\x1b[?1006h")

	feedString(tr, "\x1b[?1006l")
	prefix = string(tr.snapshotPrefix())
	assert.Contains(t, prefix, "\x1b[?1002h",
		"resetting encoding must not clear tracking mode")
	assert.NotContains(t, prefix, "\x1b[?1006h",
		"encoding mode must reset to off")
}

// TestModeTracker_OSC_BEL: OSC 0 with BEL terminator captures the
// title and round-trips through snapshotPrefix.
func TestModeTracker_OSC_BEL(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]0;hello\x07")
	prefix := tr.snapshotPrefix()
	assert.Equal(t, []byte("\x1b]0;hello\x07"), prefix,
		"OSC 0 with BEL must produce the same OSC 0 with BEL on snapshot")
}

// TestModeTracker_OSC_ST: OSC 2 with ST (\x1b\\) terminator. Less
// common than BEL but spec-compliant; some terminals emit it.
func TestModeTracker_OSC_ST(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]2;world\x1b\\")
	prefix := tr.snapshotPrefix()
	assert.Equal(t, []byte("\x1b]0;world\x07"), prefix,
		"OSC 2 must drive the same title slot as OSC 0; emission normalizes to OSC 0+BEL")
}

// TestModeTracker_OSC_BodyOverflow: an OSC body longer than oscBufCap
// must be dropped silently. Previous title (or nil) must persist.
func TestModeTracker_OSC_BodyOverflow(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]0;previous\x07")
	require.Equal(t, []byte("\x1b]0;previous\x07"), tr.snapshotPrefix())

	// 300 bytes of body — well past oscBufCap=256.
	overflow := "\x1b]0;" + strings.Repeat("X", 300) + "\x07"
	feedString(tr, overflow)
	assert.Equal(t, []byte("\x1b]0;previous\x07"), tr.snapshotPrefix(),
		"overflowed OSC body must leave the previous title intact")
}

// TestModeTracker_OSC_AbortedByNewEscape: an OSC interrupted by a fresh
// `\x1b[...` escape must abandon the OSC and parse the new sequence.
// Without this, a malformed program could silently disable mode tracking
// for everything that follows.
func TestModeTracker_OSC_AbortedByNewEscape(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]0;partial\x1b[?1049h")
	prefix := tr.snapshotPrefix()
	assert.Contains(t, string(prefix), "\x1b[?1049h",
		"the trailing CSI must be parsed even after an aborted OSC")
	assert.NotContains(t, string(prefix), "partial",
		"the abandoned OSC must not become a title")
}

// TestModeTracker_TitleEmissionAfterModes verifies the title appears at
// the very end of the prefix so it doesn't get clobbered by mode
// resets that some terminals emit on cursor visibility changes.
func TestModeTracker_TitleEmissionAfterModes(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]0;myshell\x07\x1b[?25l\x1b[?1049h")
	prefix := tr.snapshotPrefix()
	titleAt := bytes.Index(prefix, []byte("\x1b]0;"))
	altAt := bytes.Index(prefix, []byte("\x1b[?1049h"))
	require.NotEqual(t, -1, titleAt)
	require.NotEqual(t, -1, altAt)
	assert.Less(t, altAt, titleAt, "title must come after every mode fragment")
}

// TestModeTracker_OSC_OtherPsIgnored: only Ps==0 and Ps==2 affect the
// title. OSC 1 (icon name only, in xterm semantics) and unknown Ps
// codes must be ignored — emission should not pick them up.
func TestModeTracker_OSC_OtherPsIgnored(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b]1;icon\x07\x1b]7;cwd\x07")
	assert.Nil(t, tr.snapshotPrefix(),
		"OSC 1 (icon) and OSC 7 (cwd) must not become a window title")
}

// TestModeTracker_CSIInterruptedByEscape: a CSI cut short by a fresh
// `\x1b` is aborted in favor of the new escape. This matches xterm's
// "ESC always cancels" rule and ensures we don't accumulate stale params
// across a malformed sequence.
func TestModeTracker_CSIInterruptedByEscape(t *testing.T) {
	tr := &modeTracker{}
	feedString(tr, "\x1b[?25\x1b[?1049h") // first CSI lacks final byte.
	prefix := tr.snapshotPrefix()
	assert.Equal(t, []byte("\x1b[?1049h"), prefix,
		"interrupted CSI must not commit, and the new CSI must parse cleanly")
}

// TestModeTracker_NoAllocOnPlainText is a smoke test for the hot-path
// invariant: feeding ASCII text must not allocate. The tracker sits on
// every PTY chunk; a single allocation here would cost us roughly one
// per shell prompt across the worker.
func TestModeTracker_NoAllocOnPlainText(t *testing.T) {
	tr := &modeTracker{}
	plain := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 100)
	allocs := testing.AllocsPerRun(10, func() { tr.feed(plain) })
	assert.Zero(t, allocs, "plain-text feed must not allocate")
}
