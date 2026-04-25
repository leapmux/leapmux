package terminal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Corpus tests run hand-crafted but realistic PTY byte traces through
// the tracker and assert it converges to the expected end-state. They
// catch regressions that the per-mode unit tests would miss (e.g. an
// edit that only breaks one byte order, or only when interleaved with
// OSC traffic). Synthesized inline rather than captured from `script`
// because real captures vary per OS / terminal size / font and would
// make the tests environment-dependent.

// modeTrackerSnapshot is the small projection of internal tracker state
// the corpus tests assert against. Avoids exposing every field publicly
// while letting tests compare a literal struct value.
type modeTrackerSnapshot struct {
	altScreen      bool
	cursorHidden   bool
	autoWrapOff    bool
	appCursorKeys  bool
	bracketedPaste bool
	mouseTrack     mouseTrackMode
	mouseEncoding  mouseEncodingMode
	title          string
}

func snap(t *modeTracker) modeTrackerSnapshot {
	var title string
	if t.title != nil {
		title = string(t.title)
	}
	return modeTrackerSnapshot{
		altScreen:      t.altScreen,
		cursorHidden:   t.cursorHidden,
		autoWrapOff:    t.autoWrapOff,
		appCursorKeys:  t.appCursorKeys,
		bracketedPaste: t.bracketedPaste,
		mouseTrack:     t.mouseTrack,
		mouseEncoding:  t.mouseEncoding,
		title:          title,
	}
}

// TestCorpus_VimOpenEditQuit simulates a typical vim session:
// enters alt screen, hides cursor, sets app cursor keys, paints
// content, then exits all modes when the user `:q`s. The tracker
// must converge back to default state — otherwise reconnecting after
// vim quits would prefix the snapshot with stale alt-screen bytes.
func TestCorpus_VimOpenEditQuit(t *testing.T) {
	tr := &modeTracker{}

	// Enter: vim sets alt screen + hides cursor + app cursor keys.
	feedString(tr, "\x1b[?1049h\x1b[?25l\x1b[?1h")
	// Body: any number of SGR + cursor positioning + text — all opaque
	// to the tracker. SGRs in particular must not perturb state.
	for i := 0; i < 50; i++ {
		feedString(tr, "\x1b[1;1H\x1b[K\x1b[33m~\x1b[39m\r\n")
	}
	feedString(tr, "\x1b[2;5HHello, vim!\x1b[H")
	// Exit: reverse every mode the entry set.
	feedString(tr, "\x1b[?1l\x1b[?25h\x1b[?1049l")

	assert.Equal(t, modeTrackerSnapshot{}, snap(tr),
		"vim open+quit must leave the tracker at default state")
	assert.Nil(t, tr.snapshotPrefix())
}

// TestCorpus_LessScroll simulates `less` paging through a file:
// enters alt screen, paints lines, then exits. less also disables
// autowrap during paint so output doesn't soft-wrap into the next row.
func TestCorpus_LessScroll(t *testing.T) {
	tr := &modeTracker{}

	feedString(tr, "\x1b[?1049h\x1b[?7l\x1b[H")
	for line := 0; line < 24; line++ {
		feedString(tr, "this is line "+strings.Repeat("x", 60)+"\r\n")
	}
	feedString(tr, ":")
	// User quits.
	feedString(tr, "\x1b[?7h\x1b[?1049l")

	assert.Equal(t, modeTrackerSnapshot{}, snap(tr))
	assert.Nil(t, tr.snapshotPrefix())
}

// TestCorpus_HtopTick simulates htop running in alt screen for a few
// refresh cycles. htop typically enables mouse tracking + SGR encoding
// and stays in alt screen until the user quits — i.e. a snapshot
// captured mid-run must restore alt screen + mouse modes.
func TestCorpus_HtopTick(t *testing.T) {
	tr := &modeTracker{}

	// Setup.
	feedString(tr, "\x1b[?1049h\x1b[?25l\x1b[?1006h\x1b[?1002h")
	// Five ticks of repaint (cursor moves + SGR + text — all opaque).
	for tick := 0; tick < 5; tick++ {
		feedString(tr, "\x1b[1;1H\x1b[K\x1b[1;32mPID\x1b[m   USER  CPU%\r\n")
		for row := 2; row <= 20; row++ {
			feedString(tr, "\x1b["+itoa(row)+";1H\x1b[K"+strings.Repeat("data ", 12))
		}
	}

	got := snap(tr)
	assert.True(t, got.altScreen)
	assert.True(t, got.cursorHidden)
	assert.Equal(t, mouseTrackBtnEvent, got.mouseTrack)
	assert.Equal(t, mouseEncSGR, got.mouseEncoding)

	prefix := string(tr.snapshotPrefix())
	assert.Contains(t, prefix, "\x1b[?1049h")
	assert.Contains(t, prefix, "\x1b[?25l")
	assert.Contains(t, prefix, "\x1b[?1002h")
	assert.Contains(t, prefix, "\x1b[?1006h")
}

// TestCorpus_BashPromptCommand simulates a bash session whose
// $PROMPT_COMMAND emits OSC 0 on every prompt redraw — frequent enough
// that a careless OSC parser would either leak titles into the body or
// hang on the BEL terminator. The tracker must end with the most recent
// title only.
func TestCorpus_BashPromptCommand(t *testing.T) {
	tr := &modeTracker{}

	dirs := []string{"/", "/home", "/home/me", "/home/me/work", "/home/me/work/leapmux"}
	for _, d := range dirs {
		feedString(tr, "\x1b]0;me@host:"+d+"\x07")
		feedString(tr, "$ ls\r\nfile1 file2\r\n")
	}

	assert.Equal(t, "me@host:/home/me/work/leapmux", string(tr.title),
		"title must reflect the LAST OSC 0 emitted, not an earlier one")
	assert.False(t, tr.altScreen, "no alt-screen toggles in this corpus")
}

// TestCorpus_TmuxNested simulates tmux: the outer tmux enters alt
// screen via legacy code 1047 (some tmux configs prefer it for
// compatibility with old tic databases), then the inner shell uses the
// modern 1049. The tracker must collapse all three aliases — 47, 1047,
// 1049 — to a single altScreen bit and emit the canonical 1049 on
// snapshot regardless of which alias the program used.
func TestCorpus_TmuxNested(t *testing.T) {
	tr := &modeTracker{}

	feedString(tr, "\x1b[?1047h") // outer enters alt screen via 1047.
	// Some content.
	feedString(tr, "\x1b[Htmux\r\n")
	// Outer exits via 47 (rare but valid).
	feedString(tr, "\x1b[?47l")

	assert.False(t, tr.altScreen, "alt-screen aliases must all flip the same bit")
	assert.Nil(t, tr.snapshotPrefix())

	// Inner now enters alt screen via 1049.
	feedString(tr, "\x1b[?1049h")
	assert.True(t, tr.altScreen)
	assert.Equal(t, []byte("\x1b[?1049h"), tr.snapshotPrefix(),
		"emission must always normalize to 1049 regardless of which alias was used")
}

// itoa is a tiny inline integer→ASCII helper so the corpus tests don't
// pull in strconv just for cursor-position numerals.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
