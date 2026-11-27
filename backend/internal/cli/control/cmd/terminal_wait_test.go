package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

const (
	terminalReady         = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
	terminalExited        = leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED
	terminalStarting      = leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING
	terminalStartupFailed = leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED
)

// screenInfo is the record of terminal t-1 whose retained bytes are screen.
func screenInfo(cols, rows uint32, screen string) *leapmuxv1.TerminalInfo {
	return &leapmuxv1.TerminalInfo{
		TerminalId: "t-1", Cols: cols, Rows: rows, Screen: []byte(screen),
		ScreenEndOffset: int64(len(screen)), Status: terminalReady,
	}
}

// containing matches a line that holds text.
func containing(t *testing.T, text string) func(string) bool {
	t.Helper()
	match, err := textMatcher(text)
	require.NoError(t, err)
	return match
}

// matching matches a line that pattern matches.
func matching(t *testing.T, pattern string) func(string) bool {
	t.Helper()
	match, err := regexMatcher(pattern)
	require.NoError(t, err)
	return match
}

// newTestEmulator starts a screen emulator of cols by rows over screen.
func newTestEmulator(t *testing.T, cols, rows uint32, screen string) *screenEmulator {
	t.Helper()
	em, err := newScreenEmulator(screenInfo(cols, rows, screen))
	require.NoError(t, err)
	t.Cleanup(em.dispose)
	return em
}

// newTestScreen starts a screen watch of cols by rows over screen.
func newTestScreen(t *testing.T, cols, rows uint32, screen string, match func(string) bool) *screenWatch {
	t.Helper()
	w, err := newScreenWatch(screenInfo(cols, rows, screen), match, false)
	require.NoError(t, err)
	t.Cleanup(w.dispose)
	return w
}

// requireCheck checks the screen and returns the line that matched, or ""
// when none did.
func requireCheck(t *testing.T, w *screenWatch) string {
	t.Helper()
	line, ok, err := w.check()
	require.NoError(t, err)
	if !ok {
		return ""
	}
	return line
}

func requireWrite(t *testing.T, w interface{ write([]byte) error }, data string) {
	t.Helper()
	require.NoError(t, w.write([]byte(data)))
}

func dataEvent(data string, endOffset int64, snapshot bool) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Data{
		Data: &leapmuxv1.TerminalData{Data: []byte(data), EndOffset: endOffset, IsSnapshot: snapshot},
	}}
}

func closedEvent(code int32) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Closed{
		Closed: &leapmuxv1.TerminalClosed{ExitCode: &code},
	}}
}

// closedEventWithoutCode is the TerminalClosed of a shell whose exit the Worker
// holds no record of.
func closedEventWithoutCode() *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Closed{
		Closed: &leapmuxv1.TerminalClosed{},
	}}
}

func statusEvent(status leapmuxv1.TerminalStatus, startupError string) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_StatusChange{
		StatusChange: &leapmuxv1.TerminalStatusChange{Status: status, StartupError: startupError},
	}}
}

func statusEventWithExitCode(status leapmuxv1.TerminalStatus, code int32) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_StatusChange{
		StatusChange: &leapmuxv1.TerminalStatusChange{Status: status, ExitCode: &code},
	}}
}

func TestTerminalWaitSpec_RefusesACommandLineWithoutOneCondition(t *testing.T) {
	clearRemoteEnv(t)
	id := fullTestID("term-1")
	cases := []struct {
		name  string
		args  []string
		wants string
	}{
		{"no condition", nil, "missing the condition"},
		{"text and regex", []string{"--text", "a", "--regex", "b"}, "exclude each other"},
		{"exit and text", []string{"--exit", "--text", "a"}, "--exit takes no --text"},
		{"exit and regex", []string{"--exit", "--regex", "a"}, "--exit takes no --text"},
		{"an empty text", []string{"--text", ""}, "--text is empty"},
		{"a text with a line break", []string{"--text", "a\nb"}, "line break"},
		{"a text with a carriage return", []string{"--text", "a\rb"}, "line break"},
		{"an empty regex", []string{"--regex", ""}, "--regex is empty"},
		{"a regex that does not compile", []string{"--regex", "a("}, "does not compile"},
		{"a regex that matches an empty line", []string{"--regex", "^x*$"}, "matches an empty line"},
		{"a negative timeout", []string{"--exit", "--timeout", "-1s"}, "--timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"terminal", "wait", id, "--hub", "https://stub.invalid"}, tc.args...)
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.wants)
		})
	}
}

func TestTextMatcher_MatchesASubstringOfOneLine(t *testing.T) {
	match := containing(t, "PASS")
	assert.True(t, match("--- PASS: TestX"))
	assert.False(t, match("--- pass: TestX"), "the match is case sensitive")
	assert.False(t, match(""))
}

func TestRegexMatcher_AnchorsAtTheEndsOfTheLine(t *testing.T) {
	match := matching(t, "^DONE$")
	assert.True(t, match("DONE"))
	assert.False(t, match("$ make; echo DONE"), "the echoed command line does not match")
}

func TestNewScreenWatch_RefusesAScreenWithoutASize(t *testing.T) {
	_, err := newScreenWatch(screenInfo(0, 24, ""), containing(t, "x"), false)
	assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "no size")
}

func TestNewScreenWatch_RefusesNilMatcher(t *testing.T) {
	_, err := newScreenWatch(screenInfo(80, 24, ""), nil, false)
	require.Error(t, err)
	assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code)
}

func TestNewScreenWatch_RefusesAScreenTooLargeToRender(t *testing.T) {
	_, err := newScreenWatch(screenInfo(maxTerminalDimension+1, 24, ""), containing(t, "x"), false)
	assert.Contains(t, err.Error(), "too large to render")
	_, err = newScreenWatch(screenInfo(5000, 5000, ""), containing(t, "x"), false)
	assert.Contains(t, err.Error(), "too large to render", "the scrollback counts in the cells that a render allocates")
	assert.Empty(t, cli.FromError(err).Hints, "a wait has no --raw to point at")
}

// A wait reads the record of the terminal: the exit code of a shell that
// ended, the failure of a shell that did not start, or the status of a shell
// that runs.
func TestTerminalWaiterRecord(t *testing.T) {
	for name, tc := range map[string]struct {
		spec    terminalWaitSpec
		record  *leapmuxv1.TerminalInfo
		outcome terminalWaitOutcome
		done    bool
		want    terminalWaitResult
	}{
		"a wait for exit, a shell that ended with a code": {
			exitSpec, &leapmuxv1.TerminalInfo{Status: terminalExited, ExitCode: int32Ptr(0)},
			terminalWaitHeld, true, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(0), mode: terminalWaitExit},
		},
		"a wait for exit, a record of an older Worker": {
			exitSpec, &leapmuxv1.TerminalInfo{Status: terminalExited},
			terminalWaitHeld, true, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, mode: terminalWaitExit},
		},
		"a wait for text, a shell that a signal ended": {
			terminalWaitSpec{}, &leapmuxv1.TerminalInfo{Status: terminalExited, ExitCode: int32Ptr(-1)},
			terminalWaitShellEnded, true, terminalWaitResult{ID: "t-1", Status: "exited", ExitCode: int32Ptr(-1)},
		},
		"a running shell": {
			exitSpec, &leapmuxv1.TerminalInfo{Status: terminalReady},
			0, false, terminalWaitResult{ID: "t-1", Status: "ready", mode: terminalWaitExit},
		},
		"a shell that failed to start": {
			exitSpec, &leapmuxv1.TerminalInfo{Status: terminalStartupFailed, StartupError: "no shell"},
			terminalWaitStartupFailed, true, terminalWaitResult{ID: "t-1", Status: "startup_failed", StartupError: "no shell", mode: terminalWaitExit},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newTerminalWaiter("t-1", tc.spec)
			outcome, done := w.record(tc.record)
			assert.Equal(t, tc.outcome, outcome)
			assert.Equal(t, tc.done, done)
			assert.Equal(t, tc.want, w.result)
		})
	}
}

// The result keeps a copy of the exit code, so a later change of the record
// does not change what the command prints.
func TestTerminalWaiterRecord_CopiesTheExitCode(t *testing.T) {
	record := &leapmuxv1.TerminalInfo{Status: terminalExited, ExitCode: int32Ptr(3)}
	w := newTerminalWaiter("t-1", exitSpec)
	w.record(record)
	*record.ExitCode = 9
	assert.Equal(t, int32Ptr(3), w.result.ExitCode)
}

func TestScreenWatch_MatchesTheScreenAtTheStart(t *testing.T) {
	w := newTestScreen(t, 80, 24, "ok  \r\nbuild ok\r\n$ ", containing(t, "build"))
	assert.Equal(t, "build ok", requireCheck(t, w))
}

func TestScreenWatch_MatchesATextThatArrivesInTwoChunks(t *testing.T) {
	w := newTestScreen(t, 80, 24, "$ ", containing(t, "PASS"))
	requireWrite(t, w, "PA")
	assert.Empty(t, requireCheck(t, w))
	// An escape sequence splits the word too, and the screen shows no trace of it.
	requireWrite(t, w, "\x1b[1;32mS\x1b[0")
	assert.Empty(t, requireCheck(t, w))
	requireWrite(t, w, "mS\r\n")
	assert.Equal(t, "$ PASS", requireCheck(t, w))
}

func TestScreenWatch_DropsTheTrailingBlanksOfALine(t *testing.T) {
	w := newTestScreen(t, 80, 24, "", matching(t, `^\$$`))
	requireWrite(t, w, "$ ")
	assert.Equal(t, "$", requireCheck(t, w), "a line loses its trailing blanks, as terminal read prints it")
}

func TestScreenWatch_JoinsALineThatTheTerminalWrapped(t *testing.T) {
	w := newTestScreen(t, 10, 5, "", matching(t, `^0123456789abcdefghij$`))
	requireWrite(t, w, "0123456789abcdefghij\r\n")
	assert.Equal(t, "0123456789abcdefghij", requireCheck(t, w))
}

func TestScreenWatch_JoinsAWideCharacterThatMovedToTheNextRow(t *testing.T) {
	w := newTestScreen(t, 5, 5, "", containing(t, "abcd漢"))
	// The wide character takes two cells and only one is left on the row, so
	// it moves to the next row and leaves the last cell empty.
	requireWrite(t, w, "abcd漢x\r\n")
	assert.Equal(t, "abcd漢x", requireCheck(t, w))
}

func TestScreenWatch_MatchesALineThatScrolledOffWithinOneChunk(t *testing.T) {
	w := newTestScreen(t, 20, 3, "$ run\r\n", containing(t, "MARK"))
	requireWrite(t, w, "MARK\r\n1\r\n2\r\n3\r\n4\r\n5\r\n")
	assert.Equal(t, "MARK", requireCheck(t, w))
}

func TestScreenWatch_IgnoresTheScrollbackFromBeforeTheWait(t *testing.T) {
	w := newTestScreen(t, 20, 3, "OLD\r\n1\r\n2\r\n3\r\n$ ", containing(t, "OLD"))
	assert.Empty(t, requireCheck(t, w), "OLD scrolled off before the wait began")
	requireWrite(t, w, "4\r\n5\r\n")
	assert.Empty(t, requireCheck(t, w))
	requireWrite(t, w, "OLD again\r\n")
	assert.Equal(t, "OLD again", requireCheck(t, w))
}

func TestScreenWatch_ReadsALineAboveTheScreenOnce(t *testing.T) {
	var read []string
	w := newTestScreen(t, 20, 3, "", func(line string) bool {
		read = append(read, line)
		return false
	})
	requireWrite(t, w, "a\r\nb\r\nc\r\nd\r\ne")
	requireCheck(t, w)
	assert.Equal(t, []string{"e", "d", "c", "b", "a"}, read, "the first check reads the screen and the rows that scrolled above it")
	read = nil
	requireCheck(t, w)
	assert.Equal(t, []string{"e", "d", "c"}, read, "the next check reads the screen alone")
}

func TestScreenWatch_ReadsTheAlternateScreen(t *testing.T) {
	w := newTestScreen(t, 20, 5, "PROMPT\r\n", containing(t, "MENU"))
	requireWrite(t, w, "\x1b[?1049h\x1b[HMENU")
	assert.Equal(t, "MENU", requireCheck(t, w))
}

func TestScreenWatch_ReadsTheNormalScreenAfterTheAlternateOne(t *testing.T) {
	w := newTestScreen(t, 20, 5, "", containing(t, "back"))
	requireWrite(t, w, "\x1b[?1049h\x1b[Hfull screen")
	assert.Empty(t, requireCheck(t, w))
	requireWrite(t, w, "\x1b[?1049lback\r\n")
	assert.Equal(t, "back", requireCheck(t, w))
}

func TestScreenWatch_CountsEveryLineAfterAFullReset(t *testing.T) {
	// The screen starts below two rows of scrollback, so the boundary is not
	// the first row.
	w := newTestScreen(t, 20, 3, "a\r\nb\r\nc\r\nd\r\n", containing(t, "NEW"))
	requireWrite(t, w, "\x1bcNEW\r\n1\r\n2\r\n3\r\n")
	assert.Equal(t, "NEW", requireCheck(t, w), "a line that scrolled off after the reset counts")
}

func TestScreenWatch_FollowsAClearOfTheScrollback(t *testing.T) {
	w := newTestScreen(t, 20, 3, "OLD\r\n1\r\n2\r\n3\r\n", containing(t, "OLD"))
	// Erase the screen and the scrollback, as the clear command does. The rows
	// move up, and the boundary moves with them.
	requireWrite(t, w, "\x1b[H\x1b[2J\x1b[3J")
	assert.Empty(t, requireCheck(t, w))
	requireWrite(t, w, "OLD news\r\n")
	assert.Equal(t, "OLD news", requireCheck(t, w))
}

func TestScreenWatch_ResetReplacesTheWholeState(t *testing.T) {
	w := newTestScreen(t, 20, 5, "", containing(t, "SNAP"))
	require.NoError(t, w.reset([]byte("SNAP\r\n$ ")))
	assert.Equal(t, "SNAP", requireCheck(t, w))
}

func TestScreenWatch_ReturnsTheLowestLineThatMatches(t *testing.T) {
	w := newTestScreen(t, 20, 5, "ok 1\r\nok 2\r\n", containing(t, "ok"))
	assert.Equal(t, "ok 2", requireCheck(t, w))
}

func TestScreenWatch_StopsScanningAfterLowestMatch(t *testing.T) {
	t.Run("plain lines", func(t *testing.T) {
		var seen []string
		w := newTestScreen(t, 20, 4, "top\r\nok 1\r\nok 2\r\ntail", func(line string) bool {
			seen = append(seen, line)
			return strings.Contains(line, "ok")
		})
		assert.Equal(t, "ok 2", requireCheck(t, w))
		assert.Equal(t, []string{"tail", "ok 2"}, seen, "scanning stops as soon as the lowest matching line is found")
	})

	t.Run("wrapped lines", func(t *testing.T) {
		var seen []string
		w := newTestScreen(t, 6, 5, "top\r\n0123456789\r\ntail", func(line string) bool {
			seen = append(seen, line)
			return strings.Contains(line, "0123456789")
		})
		assert.Equal(t, "0123456789", requireCheck(t, w))
		assert.Equal(t, []string{"", "tail", "0123456789"}, seen)
	})
}

func screenItems(items ...screenFeedItem) <-chan screenFeedItem {
	ch := make(chan screenFeedItem, len(items))
	for _, item := range items {
		ch <- item
	}
	return ch
}

func eventItem(te *leapmuxv1.TerminalEvent) screenFeedItem {
	return screenFeedItem{event: te}
}

func textSpec(t *testing.T, text string) terminalWaitSpec {
	return terminalWaitSpec{match: containing(t, text), condition: `the text "` + text + `"`}
}

func awaitScreenText(ctx context.Context, clock quartz.Clock, items <-chan screenFeedItem, screen *screenWatch, w *terminalWaiter) (terminalWaitResult, terminalWaitOutcome, error) {
	return awaitScreen(ctx, clock, items, &screen.screenEmulator, w, screenTextCheck(screen, w))
}

func TestAwaitScreenText_EndsWhenTheOutputShowsTheText(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "PASS")
	screen := newTestScreen(t, 80, 24, "$ ", spec.match)
	items := screenItems(
		eventItem(statusEvent(terminalReady, "")),
		eventItem(dataEvent("make test\r\n", 12, false)),
		eventItem(dataEvent("--- PA", 18, false)),
		eventItem(dataEvent("SS: TestX\r\n", 29, false)),
	)
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "ready", Reached: true, Line: "--- PASS: TestX"}, result)
}

func TestAwaitScreenTextUsesTheGeometryBeforeNewOutput(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "READY")
	screen := newTestScreen(t, 6, 2, "", spec.match)
	base := &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2}
	resize := &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 2, Cols: 12, Rows: 2}
	const output = "\x1b[1;8HREADY\x1b[2;1HNOPE"
	data := dataEvent(output, int64(len(output)), false)
	data.GetData().ScreenEpoch, data.GetData().GeometryRevision = "screen", 2
	items := screenItems(
		eventItem(&leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{
			IsSnapshot: true, ScreenTimeline: &leapmuxv1.TerminalScreenTimeline{Base: base},
		}}}),
		eventItem(&leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_ScreenGeometry{ScreenGeometry: resize}}),
		eventItem(data),
		eventItem(statusEvent(terminalExited, "")),
	)
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Contains(t, result.Line, "READY")
}

func TestScreenResizePreservesTheEligibleCellsOfAWrappedLine(t *testing.T) {
	screen := newTestScreen(t, 4, 2, "0123456789AB\r\nP", matching(t, `^89AB$`))
	assert.Equal(t, "89AB", requireCheck(t, screen))
	screen.position.epoch, screen.position.revision = "screen", 1
	require.NoError(t, screen.applyScreenGeometry(&leapmuxv1.TerminalScreenGeometry{
		Epoch: "screen", Revision: 2, Offset: screen.position.offset, Cols: 12, Rows: 2,
	}, true))
	assert.Equal(t, "89AB", requireCheck(t, screen))
	screen.match = matching(t, `^0123456789AB$`)
	assert.Empty(t, requireCheck(t, screen))
	screen.match = matching(t, `^READY$`)
	requireWrite(t, screen, "\x1b[2J\x1b[1;1HREADY")
	assert.Equal(t, "READY", requireCheck(t, screen))
}

func TestScreenTimelineReplaysSeveralResizesAtOneOffset(t *testing.T) {
	screen := newTestScreen(t, 6, 2, "", containing(t, "READY"))
	const output = "\x1b[1;8HREADY\x1b[2;1HNOPE"
	require.NoError(t, screen.replayScreenTimeline(&leapmuxv1.TerminalData{
		IsSnapshot: true,
		Data:       []byte(output),
		EndOffset:  int64(len(output)),
		ScreenTimeline: &leapmuxv1.TerminalScreenTimeline{
			Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2},
			Changes: []*leapmuxv1.TerminalScreenGeometry{
				{Epoch: "screen", Revision: 2, Cols: 9, Rows: 2},
				{Epoch: "screen", Revision: 3, Cols: 12, Rows: 2},
			},
		},
	}, nil))
	assert.Contains(t, requireCheck(t, screen), "READY")
	assert.Equal(t, terminalScreenPosition{epoch: "screen", revision: 3, offset: int64(len(output))}, screen.position)
}

func TestScreenWatchReportsGeometryThatCannotReconstructRetainedBytes(t *testing.T) {
	info := screenInfo(12, 2, "READY")
	info.ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{
		Base:        &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Offset: 5, Revision: 2, Cols: 12, Rows: 2},
		StartOffset: 5,
	}
	screen, err := newScreenWatch(info, containing(t, "READY"), false)
	if screen != nil {
		defer screen.dispose()
	}
	require.Error(t, err)
	assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
}

func TestAwaitScreenTextChecksContinuationBeforeAResize(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "READY")
	screen := newTestScreen(t, 6, 2, "", spec.match)
	screen.position = terminalScreenPosition{epoch: "screen", revision: 1}
	const before = "READY"
	body := before + "\x1b[2J\x1b[1;1HNOPE"
	continuation := dataEvent(body, int64(len(body)), false)
	continuation.GetData().ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{
		Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2},
		Changes: []*leapmuxv1.TerminalScreenGeometry{
			{Epoch: "screen", Offset: int64(len(before)), Revision: 2, Cols: 12, Rows: 2},
		},
	}
	items := screenItems(eventItem(continuation), eventItem(closedEvent(17)))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, "READY", result.Line)
}

func TestAwaitScreenText_ReplaysASnapshotFromScratch(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "ready")
	screen := newTestScreen(t, 80, 24, "$ ", spec.match)
	items := screenItems(eventItem(dataEvent("server ready\r\n", 14, true)))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, "server ready", result.Line)
}

// sideEvents are the events of a terminal that carry no output and no change
// of its shell: a bell, a notification, a title and a progress report.
func sideEvents() []*leapmuxv1.TerminalEvent {
	return []*leapmuxv1.TerminalEvent{
		{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}}},
		{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Notification{Notification: &leapmuxv1.TerminalNotification{}}},
		{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{}}},
		{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Progress{Progress: &leapmuxv1.TerminalProgress{}}},
	}
}

// An event that carries no output and no change of the shell neither ends a
// wait for text nor changes its result.
func TestAwaitScreenText_GoesOnAfterTheEventsThatCarryNoOutput(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "PASS")
	screen := newTestScreen(t, 80, 24, "$ ", spec.match)
	var items []screenFeedItem
	for _, te := range sideEvents() {
		items = append(items, eventItem(te))
	}
	items = append(items, eventItem(dataEvent("PASS\r\n", 6, false)))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), screenItems(items...), screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: terminalStatusUnknown, Reached: true, Line: "$ PASS"}, result)
}

func TestAwaitScreenText_EndsWithoutTheConditionWhenTheShellExits(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "PASS")
	screen := newTestScreen(t, 80, 24, "$ ", spec.match)
	items := screenItems(eventItem(dataEvent("FAIL\r\n", 6, false)), eventItem(closedEvent(2)))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitShellEnded, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", ExitCode: int32Ptr(2)}, result)
}

func TestAwaitScreenText_MatchesTheLastOutputBeforeTheExit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "bye")
	screen := newTestScreen(t, 80, 24, "$ exit\r\n", spec.match)
	items := screenItems(eventItem(dataEvent("bye\r\n", 5, false)), eventItem(closedEvent(0)))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome, "the output that precedes the exit on the stream counts")
	assert.Equal(t, "bye", result.Line)
}

func TestAwaitScreenText_EndsWhenTheShellFailsToStart(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "PASS")
	screen := newTestScreen(t, 80, 24, "", spec.match)
	items := screenItems(eventItem(statusEvent(terminalStarting, "")), eventItem(statusEvent(terminalStartupFailed, "no such shell: /bin/nope")))
	result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitStartupFailed, outcome)
	assert.Equal(t, "startup_failed", result.Status)
	assert.Equal(t, "no such shell: /bin/nope", result.StartupError)
}

func TestAwaitScreenText_ReturnsTheFailureOfTheFeed(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	spec := textSpec(t, "PASS")

	t.Run("transport error", func(t *testing.T) {
		screen := newTestScreen(t, 80, 24, "", spec.match)
		cause := errors.New("the worker refused")
		_, _, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), screenItems(screenFeedItem{err: cause}), screen, newTerminalWaiter("t-1", spec))
		assert.ErrorIs(t, err, cause)
	})
	t.Run("not found rejection maps to closed during the wait", func(t *testing.T) {
		screen := newTestScreen(t, 80, 24, "", spec.match)
		rej := &leapmuxv1.WatchRejection{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}
		_, _, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), screenItems(screenFeedItem{rejected: rej}), screen, newTerminalWaiter("t-1", spec))
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeNotFound, e.Code)
		assert.Equal(t, cli.ExitWaitEnded, e.ExitCode())
		assert.Contains(t, e.Message, "closed during the wait")
	})
	t.Run("overflow maps to unavailable with exit wait ended", func(t *testing.T) {
		screen := newTestScreen(t, 80, 24, "", spec.match)
		_, _, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), screenItems(screenFeedItem{overflow: true}), screen, newTerminalWaiter("t-1", spec))
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeUnavailable, e.Code)
		assert.Equal(t, cli.ExitWaitEnded, e.ExitCode())
		assert.Contains(t, e.Message, "too much screen output before replay finished")
	})
}

func TestAwaitScreenText_EndsWhenTheTimeoutPasses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(terminalWaitTimerTag)
	defer newTimer.Close()
	spec := textSpec(t, "PASS")
	spec.timeout = 2 * time.Minute
	screen := newTestScreen(t, 80, 24, "", spec.match)
	type ended struct {
		result  terminalWaitResult
		outcome terminalWaitOutcome
		err     error
	}
	done := make(chan ended, 1)
	go func() {
		result, outcome, err := awaitScreenText(ctx, clock, make(chan screenFeedItem), screen, newTerminalWaiter("t-1", spec))
		done <- ended{result, outcome, err}
	}()
	assert.Equal(t, 2*time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
	clock.Advance(2 * time.Minute).MustWait(ctx)
	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, terminalWaitTimedOut, got.outcome)
	assert.False(t, got.result.Reached)
}

func TestAwaitScreenText_ArmsNoTimerWithoutATimeout(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	spec := textSpec(t, "PASS")
	screen := newTestScreen(t, 80, 24, "", spec.match)
	_, _, err := awaitScreenText(ctx, clock, screenItems(eventItem(dataEvent("PASS", 4, false))), screen, newTerminalWaiter("t-1", spec))
	require.NoError(t, err)
	_, armed := clock.Peek()
	assert.False(t, armed)
}

// statusReads answers each status read of awaitShellExit from the channel
// that the test feeds, and reports each read as it starts.
type statusReads struct {
	started chan struct{}
	answers chan terminalStatusRead
}

func newStatusReads() *statusReads {
	return &statusReads{started: make(chan struct{}, 8), answers: make(chan terminalStatusRead, 8)}
}

func (r *statusReads) read(ctx context.Context) (*leapmuxv1.TerminalInfo, error) {
	select {
	case r.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case answer := <-r.answers:
		return answer.info, answer.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func feedOutputs(outs ...feedOutput) chan feedOutput {
	ch := make(chan feedOutput, len(outs)+8)
	for _, out := range outs {
		ch <- out
	}
	return ch
}

func terminalFeedEvent(te *leapmuxv1.TerminalEvent) feedOutput {
	return feedOutput{kind: feedTerminalEvent, terminal: te}
}

var exitSpec = terminalWaitSpec{mode: terminalWaitExit}

func TestAwaitShellExit_PrintsTheExitCodeOfTheShell(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	feed := feedOutputs(terminalFeedEvent(statusEvent(terminalReady, "")), terminalFeedEvent(closedEvent(3)))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(3), mode: terminalWaitExit}, result)
}

// A TerminalClosed with no code is a shell that ended without a record of its
// exit, not one that exited with 0: the wait ends, and prints no code.
func TestAwaitShellExit_PrintsNoExitCodeThatTheWorkerDoesNotRecord(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	feed := feedOutputs(terminalFeedEvent(statusEvent(terminalReady, "")), terminalFeedEvent(closedEventWithoutCode()))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, mode: terminalWaitExit}, result)
}

// A notify watch replays nothing, so a shell that ended before the Worker
// registered the watch shows only in the read of the record after the
// acknowledgement. The record states the exit code, unless the Worker holds no
// record of the exit.
func TestAwaitShellExit_ReadsTheRecordAfterEachFirstAcknowledgement(t *testing.T) {
	for name, tc := range map[string]struct {
		record *leapmuxv1.TerminalInfo
		want   terminalWaitResult
	}{
		"a record with the exit code": {
			&leapmuxv1.TerminalInfo{TerminalId: "t-1", Status: terminalExited, ExitCode: int32Ptr(7)},
			terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(7), mode: terminalWaitExit},
		},
		"a record with no exit code": {
			&leapmuxv1.TerminalInfo{TerminalId: "t-1", Status: terminalExited},
			terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, mode: terminalWaitExit},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := testutil.DeadlineContext(t)
			reads := newStatusReads()
			feed := feedOutputs(feedOutput{kind: feedLevels, first: false}, feedOutput{kind: feedLevels, first: true})
			reads.answers <- terminalStatusRead{info: tc.record}
			result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, reads.read, newTerminalWaiter("t-1", exitSpec))
			require.NoError(t, err)
			assert.Equal(t, terminalWaitHeld, outcome)
			assert.Len(t, reads.started, 1, "only the first levels of a session start a read")
			assert.Equal(t, tc.want, result)
		})
	}
}

// A status change to EXITED states no exit code, and the TerminalClosed of
// the same exit can arrive after it. The wait reads the record instead of
// ending without the code.
func TestAwaitShellExit_ReadsTheRecordAfterAStatusChangeToExited(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	reads := newStatusReads()
	feed := feedOutputs(terminalFeedEvent(statusEvent(terminalExited, "")))
	var (
		wg      sync.WaitGroup
		waitErr error
	)
	wg.Go(func() {
		select {
		case <-reads.started:
			reads.answers <- terminalStatusRead{info: &leapmuxv1.TerminalInfo{TerminalId: "t-1", Status: terminalExited, ExitCode: int32Ptr(4)}}
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	defer func() {
		cancel()
		wg.Wait()
	}()
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, reads.read, newTerminalWaiter("t-1", exitSpec))
	wg.Wait()
	require.NoError(t, waitErr)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(4), mode: terminalWaitExit}, result)
}

// The TerminalClosed that follows the status change can end the wait before
// the read of the record returns, or after it. Both carry the exit code, so
// the result is the same in either order.
func TestAwaitShellExit_EndsWithTheCodeWhenAClosedEventFollowsAnExitedStatus(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	reads := newStatusReads()
	reads.answers <- terminalStatusRead{info: &leapmuxv1.TerminalInfo{TerminalId: "t-1", Status: terminalExited, ExitCode: int32Ptr(4)}}
	feed := feedOutputs(terminalFeedEvent(statusEvent(terminalExited, "")), terminalFeedEvent(closedEvent(4)))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, reads.read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(4), mode: terminalWaitExit}, result)
}

func TestAwaitShellExit_GoesOnAfterAStatusReadOfARunningShell(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	reads := newStatusReads()
	feed := feedOutputs(feedOutput{kind: feedLevels, first: true})
	var (
		wg      sync.WaitGroup
		waitErr error
	)
	wg.Go(func() {
		select {
		case <-reads.started:
			reads.answers <- terminalStatusRead{info: &leapmuxv1.TerminalInfo{TerminalId: "t-1", Status: terminalReady}}
			feed <- feedOutput{kind: feedDown, err: errWatchEnded}
			feed <- terminalFeedEvent(closedEvent(0))
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	defer func() {
		cancel()
		wg.Wait()
	}()
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, reads.read, newTerminalWaiter("t-1", exitSpec))
	wg.Wait()
	require.NoError(t, waitErr)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, int32Ptr(0), result.ExitCode)
}

func TestAwaitShellExit_CancelsAndJoinsInFlightStatusReadOnExit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	readStatus := func(readCtx context.Context) (*leapmuxv1.TerminalInfo, error) {
		started <- struct{}{}
		<-readCtx.Done()
		canceled <- struct{}{}
		return nil, readCtx.Err()
	}
	feed := feedOutputs(feedOutput{kind: feedLevels, first: true})
	var (
		wg      sync.WaitGroup
		waitErr error
	)
	wg.Go(func() {
		select {
		case <-started:
			feed <- terminalFeedEvent(closedEvent(9))
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, readStatus, newTerminalWaiter("t-1", exitSpec))
	wg.Wait()
	require.NoError(t, waitErr)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, int32Ptr(9), result.ExitCode)
	select {
	case <-canceled:
	default:
		t.Fatal("awaitShellExit returned before canceling and joining the in-flight status read")
	}
}

// The events that carry no change of the shell, and the events of agents,
// neither end a wait for exit nor start a read of the record.
func TestAwaitShellExit_GoesOnAfterTheEventsThatStateNoExit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	reads := newStatusReads()
	var outs []feedOutput
	for _, te := range sideEvents() {
		outs = append(outs, terminalFeedEvent(te))
	}
	outs = append(outs, feedOutput{kind: feedAgentEvent}, terminalFeedEvent(closedEvent(5)))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feedOutputs(outs...), reads.read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "exited", Reached: true, ExitCode: int32Ptr(5), mode: terminalWaitExit}, result)
	assert.Empty(t, reads.started, "no event before the exit started a read of the record")
}

func TestAwaitShellExit_ReturnsAFailedStatusRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	reads := newStatusReads()
	cause := errors.New("the worker did not answer")
	reads.answers <- terminalStatusRead{err: cause}
	_, _, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feedOutputs(feedOutput{kind: feedLevels, first: true}), reads.read, newTerminalWaiter("t-1", exitSpec))
	assert.ErrorIs(t, err, cause)
}

func TestAwaitShellExit_EndsWithoutTheConditionWhenTheShellFailsToStart(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	feed := feedOutputs(terminalFeedEvent(statusEvent(terminalStartupFailed, "spawn failed")))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitStartupFailed, outcome)
	assert.Equal(t, "spawn failed", result.StartupError)
	assert.False(t, result.Reached)
}

func TestAwaitShellExit_EndsWhenTheTerminalCloses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	feed := feedOutputs(
		// A rejection that the subscription retries by itself, and one of an
		// agent, change nothing.
		feedOutput{kind: feedRejected, rejectedTerminal: true, rejection: &leapmuxv1.WatchRejection{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED}},
		feedOutput{kind: feedRejected, rejection: &leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}},
		feedOutput{kind: feedRejected, rejectedTerminal: true, rejection: &leapmuxv1.WatchRejection{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}},
	)
	_, _, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeNotFound, e.Code)
	assert.Equal(t, cli.ExitWaitEnded, e.ExitCode(), "a terminal that closed ends the wait without its condition")
}

func TestAwaitShellExit_ReturnsARefusal(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	cause := errors.New("permission denied")
	_, _, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feedOutputs(feedOutput{kind: feedRefused, err: cause}), newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	assert.ErrorIs(t, err, cause)
}

func TestAwaitShellExit_EndsWhenTheTimeoutPasses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(terminalWaitTimerTag)
	defer newTimer.Close()
	spec := terminalWaitSpec{mode: terminalWaitExit, timeout: time.Minute}
	type ended struct {
		result  terminalWaitResult
		outcome terminalWaitOutcome
		err     error
	}
	done := make(chan ended, 1)
	go func() {
		result, outcome, err := awaitShellExit(ctx, clock, feedOutputs(terminalFeedEvent(statusEvent(terminalReady, ""))), newStatusReads().read, newTerminalWaiter("t-1", spec))
		done <- ended{result, outcome, err}
	}()
	assert.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
	clock.Advance(time.Minute).MustWait(ctx)
	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, terminalWaitTimedOut, got.outcome)
	assert.Equal(t, terminalWaitResult{ID: "t-1", Status: "ready", mode: terminalWaitExit}, got.result)
}

func TestAwaitShellExit_EndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	cancel()
	_, _, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), make(chan feedOutput), newStatusReads().read, newTerminalWaiter("t-1", exitSpec))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestTerminalWaitResult_RenderText(t *testing.T) {
	cases := []struct {
		name   string
		result terminalWaitResult
		want   string
	}{
		{"an exit code", terminalWaitResult{Reached: true, ExitCode: int32Ptr(3), mode: terminalWaitExit}, "3\n"},
		{"an exit without a code", terminalWaitResult{Reached: true, mode: terminalWaitExit}, ""},
		{"a screen change", terminalWaitResult{Reached: true, Screen: "$ hello", mode: terminalWaitChange}, "$ hello\n"},
		{"a screen change to an empty screen", terminalWaitResult{Reached: true, mode: terminalWaitChange}, ""},
		{"a matched line", terminalWaitResult{Reached: true, Line: "--- PASS"}, "--- PASS\n"},
		{"a wait for text that ended with the shell", terminalWaitResult{ExitCode: int32Ptr(1)}, ""},
		{"a wait that timed out", terminalWaitResult{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, tc.result.RenderText(&out))
			assert.Equal(t, tc.want, out.String())
		})
	}
}

func TestTerminalWaitEnded_SaysWhyOnStderrAndExitsSix(t *testing.T) {
	cases := []struct {
		name    string
		result  terminalWaitResult
		outcome terminalWaitOutcome
		spec    terminalWaitSpec
		want    string
	}{
		{"a wait for exit that timed out", terminalWaitResult{ID: "t-1", mode: terminalWaitExit}, terminalWaitTimedOut, terminalWaitSpec{mode: terminalWaitExit, timeout: time.Minute},
			"the wait ended after 1m0s, before the shell of terminal t-1 ended"},
		{"a wait for text that timed out", terminalWaitResult{ID: "t-1"}, terminalWaitTimedOut, terminalWaitSpec{condition: `the text "PASS"`, timeout: time.Minute},
			`the wait ended after 1m0s, before terminal t-1 showed the text "PASS"`},
		{"a shell that exited", terminalWaitResult{ID: "t-1", ExitCode: int32Ptr(2)}, terminalWaitShellEnded, terminalWaitSpec{condition: `the text "PASS"`},
			`the shell of terminal t-1 exited with code 2 before the terminal showed the text "PASS"`},
		{"a shell that ended without a code", terminalWaitResult{ID: "t-1"}, terminalWaitShellEnded, terminalWaitSpec{condition: `the text "PASS"`},
			`the shell of terminal t-1 ended before the terminal showed the text "PASS"`},
		{"a shell that failed to start", terminalWaitResult{ID: "t-1", StartupError: "no shell"}, terminalWaitStartupFailed, terminalWaitSpec{mode: terminalWaitExit},
			"the shell of terminal t-1 failed to start: no shell"},
		{"a shell that failed to start without a reason", terminalWaitResult{ID: "t-1"}, terminalWaitStartupFailed, terminalWaitSpec{mode: terminalWaitExit},
			"the shell of terminal t-1 failed to start\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := terminalWaitEnded(&cli.Printer{Out: &out, Err: &errOut}, tc.result, tc.outcome, tc.spec)
			var status *cli.ExitStatus
			require.ErrorAs(t, err, &status)
			assert.Equal(t, cli.ExitWaitEnded, status.Code)
			assert.Contains(t, errOut.String(), tc.want)
			assert.True(t, strings.HasSuffix(errOut.String(), "\n"))
		})
	}
}

func TestTerminalWaitEnded_ReceiptOnlyOnTimeout(t *testing.T) {
	spec := terminalWaitSpec{
		condition:    `the text "PASS"`,
		timeout:      time.Minute,
		requestID:    "req-123",
		retryCommand: "leapmux terminal run t-1 --wait --text PASS --request-id req-123 'make test'",
	}

	t.Run("timeout attaches receipt and prints notices", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := terminalWaitEnded(&cli.Printer{Format: cli.FormatJSON, Out: &out, Err: &errOut}, terminalWaitResult{ID: "t-1", RequestID: "req-123", Status: "ready"}, terminalWaitTimedOut, spec)
		var status *cli.ExitStatus
		require.ErrorAs(t, err, &status)
		assert.Equal(t, cli.ExitWaitEnded, status.Code)
		var env struct {
			Data terminalWaitResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &env))
		assert.Equal(t, "req-123", env.Data.RequestID)
		require.NotNil(t, env.Data.Receipt)
		assert.Equal(t, dispositionInProgress, env.Data.Receipt.Disposition)
		assert.Equal(t, spec.retryCommand, env.Data.Receipt.RetryCommand)
		assert.Contains(t, errOut.String(), "disposition: in_progress (request_id: req-123)")
		assert.Contains(t, errOut.String(), "safe retry: "+spec.retryCommand)
	})

	for name, outcome := range map[string]terminalWaitOutcome{
		"shell ended":    terminalWaitShellEnded,
		"startup failed": terminalWaitStartupFailed,
	} {
		t.Run(name+" omits receipt and safe retry", func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := terminalWaitEnded(&cli.Printer{Format: cli.FormatJSON, Out: &out, Err: &errOut}, terminalWaitResult{ID: "t-1", RequestID: "req-123", Status: "exited"}, outcome, spec)
			var status *cli.ExitStatus
			require.ErrorAs(t, err, &status)
			assert.Equal(t, cli.ExitWaitEnded, status.Code)
			var env struct {
				Data terminalWaitResult `json:"data"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &env))
			assert.Equal(t, "req-123", env.Data.RequestID)
			assert.Nil(t, env.Data.Receipt)
			assert.NotContains(t, errOut.String(), "disposition:")
			assert.NotContains(t, errOut.String(), "safe retry:")
		})
	}
}

func TestRecoverTerminalEmulator_ConvertsPanicToInternalError(t *testing.T) {
	run := func() (err error) {
		defer recoverTerminalEmulator("read the screen of terminal t-1", &err)
		panic("bad escape")
	}
	err := run()
	require.Error(t, err)
	assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "the terminal emulator failed to read the screen of terminal t-1: bad escape")
}

func TestScreenEmulator_DiffVisibleScreen(t *testing.T) {
	t.Run("unchanged blank screen reports no diff", func(t *testing.T) {
		em := newTestEmulator(t, 20, 4, "")
		cur, changed, err := em.diffVisibleScreen(nil)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Empty(t, cur)
	})

	t.Run("new line below empty lines reports diff", func(t *testing.T) {
		em := newTestEmulator(t, 20, 4, "\r\n\r\nhello")
		cur, changed, err := em.diffVisibleScreen(nil)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "\n\nhello", cur)
	})

	t.Run("no-op OSC sequence reports no diff and text change reports diff", func(t *testing.T) {
		em := newTestEmulator(t, 20, 4, "line1\r\n\r\nline3")
		initial, err := em.visibleLines()
		require.NoError(t, err)
		requireWrite(t, em, "\x1b]0;title\x07")
		_, changed, err := em.diffVisibleScreen(initial)
		require.NoError(t, err)
		assert.False(t, changed)

		requireWrite(t, em, "!")
		cur, changed, err := em.diffVisibleScreen(initial)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "line1\n\nline3!", cur)
	})

	t.Run("clearing a trailing line reports diff", func(t *testing.T) {
		em := newTestEmulator(t, 20, 4, "a\r\n\r\nb")
		initial, err := em.visibleLines()
		require.NoError(t, err)
		requireWrite(t, em, "\x1b[3;1H\x1b[2K")
		cur, changed, err := em.diffVisibleScreen(initial)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "a", cur)
	})
}

func TestWaitForScreen_PreOpenBaselineMatchesExistingOutputImmediately(t *testing.T) {
	info := &leapmuxv1.TerminalInfo{
		TerminalId:      "t-1",
		Cols:            80,
		Rows:            24,
		Status:          terminalReady,
		Screen:          []byte("welcome\r\n"),
		ScreenEndOffset: 9,
	}
	spec := terminalWaitSpec{mode: terminalWaitChange, preOpenBaseline: true, timeout: time.Second}
	res, outcome, err := waitForScreen(nil, tabTarget{ID: "t-1"}, info, spec)
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.True(t, res.Reached)
	assert.True(t, res.Changed)
	assert.Equal(t, "ready", res.Status)
	assert.Equal(t, "welcome", res.Screen)
	assert.Equal(t, int64(9), res.ScreenEndOffset)
}

func TestConsumeScreenOutput_SkipsRedundantFinalObserveWhenNoTrailingBytes(t *testing.T) {
	screen := newTestEmulator(t, 6, 2, "")
	screen.position = terminalScreenPosition{epoch: "screen", revision: 1}
	calls := 0
	err := screen.consumeScreenOutput(&leapmuxv1.TerminalData{
		Data:      []byte("hi"),
		EndOffset: 2,
		ScreenTimeline: &leapmuxv1.TerminalScreenTimeline{
			Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2},
			Changes: []*leapmuxv1.TerminalScreenGeometry{
				{Epoch: "screen", Offset: 2, Revision: 2, Cols: 12, Rows: 2},
			},
		},
	}, func() (bool, error) {
		calls++
		return false, nil
	})
	require.NoError(t, err)
	// Once after writing "hi" before the resize, once after the resize; no extra
	// third call because zero trailing bytes remained after the resize.
	assert.Equal(t, 2, calls)
}

func TestAwaitShellExit_ResolvesImmediatelyOnExitedStatusWithExitCode(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	reads := newStatusReads()
	feed := feedOutputs(terminalFeedEvent(statusEventWithExitCode(terminalExited, 42)))
	result, outcome, err := awaitShellExit(ctx, testutil.NewQuartzMock(t), feed, reads.read, newTerminalWaiter("t-1", exitSpec))
	require.NoError(t, err)
	assert.Equal(t, terminalWaitHeld, outcome)
	assert.Equal(t, terminalWaitResult{
		ID:       "t-1",
		Status:   "exited",
		Reached:  true,
		ExitCode: int32Ptr(42),
		mode:     terminalWaitExit,
	}, result)
	assert.Empty(t, reads.started, "StatusChange with exit_code must finish without calling ListTerminals")
}

func TestAwaitScreen_CapturesExitCodeFromExitedStatusChange(t *testing.T) {
	t.Run("text wait", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		spec := textSpec(t, "PASS")
		screen := newTestScreen(t, 80, 24, "$ ", spec.match)
		items := screenItems(eventItem(statusEventWithExitCode(terminalExited, 42)))
		result, outcome, err := awaitScreenText(ctx, testutil.NewQuartzMock(t), items, screen, newTerminalWaiter("t-1", spec))
		require.NoError(t, err)
		assert.Equal(t, terminalWaitShellEnded, outcome)
		assert.Equal(t, terminalWaitResult{
			ID:       "t-1",
			Status:   "exited",
			ExitCode: int32Ptr(42),
		}, result)
	})

	t.Run("change wait", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		em := newTestEmulator(t, 80, 24, "$ ")
		initialLines, err := em.visibleLines()
		require.NoError(t, err)
		spec := terminalWaitSpec{mode: terminalWaitChange, condition: "a screen change"}
		w := newTerminalWaiter("t-1", spec)
		items := screenItems(eventItem(statusEventWithExitCode(terminalExited, 42)))
		result, outcome, err := awaitScreen(ctx, testutil.NewQuartzMock(t), items, em, w, screenChangeCheck(em, w, initialLines))
		require.NoError(t, err)
		assert.Equal(t, terminalWaitShellEnded, outcome)
		assert.Equal(t, "exited", result.Status)
		assert.Equal(t, int32Ptr(42), result.ExitCode)
		assert.False(t, result.Reached)
	})
}

func TestTerminalRun_WaitOnlyFlagsRequireWaitBeforeDialing(t *testing.T) {
	clearRemoteEnv(t)
	id := fullTestID("term-run-wait")
	cases := []struct {
		flag string
		args []string
	}{
		{"exit", []string{"--exit"}},
		{"text", []string{"--text", "DONE"}},
		{"regex", []string{"--regex", "^DONE$"}},
		{"change", []string{"--change"}},
		{"scrollback", []string{"--scrollback"}},
		{"timeout", []string{"--timeout", "5s"}},
		{"after-offset", []string{"--after-offset", "10"}},
	}
	for _, tc := range cases {
		t.Run("--"+tc.flag, func(t *testing.T) {
			args := append([]string{"terminal", "run", id, "echo hi", "--hub", "https://stub.invalid"}, tc.args...)
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "--"+tc.flag+" needs --wait")
		})
	}
}
