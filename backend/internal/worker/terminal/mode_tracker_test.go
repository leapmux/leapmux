package terminal

import (
	"bytes"
	"strconv"
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

func feedAndSignals(t *modeTracker, s string) []Signal {
	t.beginChunk()
	t.feed([]byte(s))
	return t.drainSignals()
}

// TestModeTracker_PerModeSetReset is the table that covers every tracked
// DEC private mode end-to-end: the canonical set sequence flips the
// field, the canonical reset sequence flips it back, and snapshotPrefix
// reflects the current state.
func TestModeTracker_PerModeSetReset(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	tr := &modeTracker{}
	feedString(tr, "\x1b[31m\x1b[2J\x1b[5n\x1b[H")
	assert.Nil(t, tr.snapshotPrefix(),
		"non-h/l CSIs and non-DEC params must not change tracker state")
}

// TestModeTracker_MixedPrintableAndCSI: printable text bracketing a CSI
// must not pollute state. Verifies that ground-state bytes are truly
// no-ops, not accidentally mutating the tracker.
func TestModeTracker_MixedPrintableAndCSI(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	tr := &modeTracker{}
	assert.Nil(t, tr.snapshotPrefix())
}

// TestModeTracker_EmissionOrdering verifies the documented prefix
// ordering: alt-screen first (so subsequent mode changes land on the
// right buffer), then cursor, autowrap, app-cursor-keys, bracketed
// paste, mouse track, mouse encoding, then title.
func TestModeTracker_EmissionOrdering(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	tr := &modeTracker{}
	feedString(tr, "\x1b]0;hello\x07")
	prefix := tr.snapshotPrefix()
	assert.Equal(t, []byte("\x1b]0;hello\x07"), prefix,
		"OSC 0 with BEL must produce the same OSC 0 with BEL on snapshot")
}

// TestModeTracker_OSC_ST: OSC 2 with ST (\x1b\\) terminator. Less
// common than BEL but spec-compliant; some terminals emit it.
func TestModeTracker_OSC_ST(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	feedString(tr, "\x1b]2;world\x1b\\")
	prefix := tr.snapshotPrefix()
	assert.Equal(t, []byte("\x1b]0;world\x07"), prefix,
		"OSC 2 must drive the same title slot as OSC 0; emission normalizes to OSC 0+BEL")
}

// TestModeTracker_OSC_BodyOverflow: an OSC body longer than oscBufCap
// must be dropped silently. Previous title (or nil) must persist.
func TestModeTracker_OSC_BodyOverflow(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	feedString(tr, "\x1b]0;previous\x07")
	require.Equal(t, []byte("\x1b]0;previous\x07"), tr.snapshotPrefix())

	// 2100 bytes of body — well past oscBufCap=2048.
	overflow := "\x1b]0;" + strings.Repeat("X", 2100) + "\x07"
	feedString(tr, overflow)
	assert.Equal(t, []byte("\x1b]0;previous\x07"), tr.snapshotPrefix(),
		"overflowed OSC body must leave the previous title intact")

	// Parser must recover for the next valid sequence after an overrun.
	feedString(tr, "\x1b[?1049h")
	prefix := tr.snapshotPrefix()
	assert.Contains(t, string(prefix), "\x1b[?1049h")
	assert.Contains(t, string(prefix), "previous")
}

// TestModeTracker_OSC_AbortedByNewEscape: an OSC interrupted by a fresh
// `\x1b[...` escape must abandon the OSC and parse the new sequence.
// Without this, a malformed program could silently disable mode tracking
// for everything that follows.
func TestModeTracker_OSC_AbortedByNewEscape(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	// NOT t.Parallel(): testing.AllocsPerRun pins GOMAXPROCS to 1 for the
	// duration of its measurement and documents that its result is
	// unreliable when other tests run concurrently -- a sibling's
	// allocations land in this count.

	tr := &modeTracker{}
	plain := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 100)
	allocs := testing.AllocsPerRun(10, func() { tr.feed(plain) })
	assert.Zero(t, allocs, "plain-text feed must not allocate")
}

// TestModeTracker_NoAllocOnTypicalShellOutput exercises a chunk shaped
// like real shell output — OSC 0 title + SGR-coloured prompt + plain
// text + CRLF — to catch regressions the plain-text test would miss
// (e.g. a stray `[]byte(...)` conversion inside dispatchCSI, or an
// OSC body path that stopped reusing t.title's backing array). The
// initial feed is a warmup so the title slice reaches its steady-state
// capacity; from there, repeated feeds of the same chunk must allocate
// nothing.
func TestModeTracker_NoAllocOnTypicalShellOutput(t *testing.T) {
	// NOT t.Parallel(): testing.AllocsPerRun pins GOMAXPROCS to 1 for the
	// duration of its measurement and documents that its result is
	// unreliable when other tests run concurrently -- a sibling's
	// allocations land in this count.

	tr := &modeTracker{}
	chunk := []byte("\x1b]0;me@host\x07\x1b[1;32muser@host\x1b[m:~$ ls\r\n")
	tr.feed(chunk)
	allocs := testing.AllocsPerRun(10, func() { tr.feed(chunk) })
	assert.Zero(t, allocs, "steady-state mixed-CSI+OSC feed must not allocate")
}

func TestModeTracker_BellGroundState(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalBell, sigs[0].Kind)

	// BEL terminating OSC 0 must not also emit a bell signal.
	sigs = feedAndSignals(tr, "\x1b]0;title\x07")
	for _, s := range sigs {
		assert.NotEqual(t, SignalBell, s.Kind, "OSC BEL terminator must not emit SignalBell")
	}
}

func TestModeTracker_BellSplitAcrossFeeds(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	tr.beginChunk()
	tr.feed([]byte("\x07"))
	tr.feed([]byte("x"))
	sigs := tr.drainSignals()
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalBell, sigs[0].Kind)
}

func TestModeTracker_SignalTitleOSC0And2(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]0;hello\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalTitle, sigs[0].Kind)
	assert.Equal(t, "hello", sigs[0].Title)

	sigs = feedAndSignals(tr, "\x1b]2;world\x1b\\")
	require.Len(t, sigs, 1)
	assert.Equal(t, "world", sigs[0].Title)

	sigs = feedAndSignals(tr, "\x1b]0;world\x07")
	assert.Empty(t, sigs, "re-emitting the same title emits nothing")
}

func TestModeTracker_SignalTitleOSC1Ignored(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]1;icon\x07")
	assert.Empty(t, sigs)
	assert.Nil(t, tr.snapshotPrefix())
}

func TestModeTracker_SignalNotificationOSC9(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]9;hello\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalNotification, sigs[0].Kind)
	assert.Empty(t, sigs[0].Title)
	assert.Equal(t, "hello", sigs[0].Body)
}

func TestModeTracker_SignalProgressOSC9(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]9;4;1;42\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalProgress, sigs[0].Kind)
	assert.Equal(t, ProgressNormal, sigs[0].State)
	assert.Equal(t, int32(42), sigs[0].Percent)

	sigs = feedAndSignals(tr, "\x1b]9;4;0\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, ProgressClear, sigs[0].State)
}

// TestModeTracker_SignalProgressOSC9PercentClamped pins the overflow fix: an
// out-of-range percent must clamp to the documented 0..100 range instead of
// narrowing an arbitrary int to int32 (which silently wrapped to a bogus /
// negative value on the wire).
func TestModeTracker_SignalProgressOSC9PercentClamped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		seq    string
		expect int32
	}{
		{"over range clamps to 100", "\x1b]9;4;1;9999999999\x07", 100},
		{"negative clamps to 0", "\x1b]9;4;1;-5\x07", 0},
		{"boundary 100", "\x1b]9;4;1;100\x07", 100},
		{"boundary 0", "\x1b]9;4;1;0\x07", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := &modeTracker{}
			sigs := feedAndSignals(tr, tc.seq)
			require.Len(t, sigs, 1)
			assert.Equal(t, SignalProgress, sigs[0].Kind)
			assert.Equal(t, tc.expect, sigs[0].Percent)
		})
	}
}

// TestModeTracker_KittyDroppedBounded pins the unbounded-growth fix: the
// banned-id set must cap at kittyMaxDropped rather than accumulating one entry
// per distinct oversized notification for the tracker's lifetime.
func TestModeTracker_KittyDroppedBounded(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	// Emit more distinct oversized kitty notifications than the cap. Each uses a
	// unique id and a single done payload exceeding kittyMaxBytes so it is banned.
	oversized := strings.Repeat("x", kittyMaxBytes+1)
	for i := 0; i < kittyMaxDropped+40; i++ {
		feedAndSignals(tr, "\x1b]99;i="+strconv.Itoa(i)+";"+oversized+"\x07")
	}
	assert.LessOrEqual(t, len(tr.kittyDropped), kittyMaxDropped,
		"kittyDropped must be capped, not unbounded")
}

func TestModeTracker_SignalNotificationOSC777(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]777;notify;Title;Body;with;semicolons\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, "Title", sigs[0].Title)
	assert.Equal(t, "Body;with;semicolons", sigs[0].Body)
}

func TestModeTracker_SignalNotificationOSC99(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]99;i=1:d=1:p=title;Hello\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, "Hello", sigs[0].Title)

	sigs = feedAndSignals(tr, "\x1b]99;i=2:d=0:p=title;Part1\x07")
	assert.Empty(t, sigs)
	sigs = feedAndSignals(tr, "\x1b]99;i=2:d=1:p=body;Part2\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, "Part1", sigs[0].Title)
	assert.Equal(t, "Part2", sigs[0].Body)

	sigs = feedAndSignals(tr, "\x1b]99;i=3:d=1:e=1:p=title;SGVsbG8=\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, "Hello", sigs[0].Title)

	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=4:d=1:p=?;ignored\x07"))
}

func TestModeTracker_SignalNotificationOSC99Overflow(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	chunk := strings.Repeat("x", 1500)
	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=big:d=0:p=body;"+chunk+"\x07"))
	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=big:d=0:p=body;"+chunk+"\x07"))
	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=big:d=0:p=body;"+chunk+"\x07"))
	require.Contains(t, tr.kittyDropped, "big")
	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=big:d=1:p=body;more\x07"))
}

func TestModeTracker_SignalNotificationOSC99MaxPending(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	for i := 0; i < kittyMaxPending; i++ {
		id := string(rune('a' + i))
		assert.Empty(t, feedAndSignals(tr, "\x1b]99;i="+id+":d=0:p=title;chunk\x07"))
	}
	assert.Empty(t, feedAndSignals(tr, "\x1b]99;i=overflow:d=0:p=title;drop\x07"))
}

func TestModeTracker_BellCoalescingPerChunk(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	tr.beginChunk()
	for i := 0; i < 100; i++ {
		tr.feed([]byte{'\x07'})
	}
	sigs := tr.drainSignals()
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalBell, sigs[0].Kind)
}

func TestModeTracker_TitleCoalescingPerChunk(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]0;first\x07\x1b]0;last\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, "last", sigs[0].Title)
}

func TestModeTracker_ProgressCoalescingPerChunk(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	sigs := feedAndSignals(tr, "\x1b]9;4;1;10\x07\x1b]9;4;1;90\x07")
	require.Len(t, sigs, 1)
	assert.Equal(t, SignalProgress, sigs[0].Kind)
	assert.Equal(t, ProgressNormal, sigs[0].State)
	assert.Equal(t, int32(90), sigs[0].Percent)
}

func TestModeTracker_MaxNotificationsPerChunk(t *testing.T) {
	t.Parallel()

	tr := &modeTracker{}
	tr.beginChunk()
	for i := 0; i < maxNotificationsPerChunk+5; i++ {
		tr.feed([]byte("\x1b]9;body\x07"))
	}
	sigs := tr.drainSignals()
	require.Len(t, sigs, maxNotificationsPerChunk)
	for _, s := range sigs {
		assert.Equal(t, SignalNotification, s.Kind)
	}
}
