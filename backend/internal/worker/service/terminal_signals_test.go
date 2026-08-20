package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/validate"
)

func startTerminalWithOutputFn(t *testing.T, svc *Service, terminalID string) {
	t.Helper()
	ctx := context.Background()
	workingDir := t.TempDir()
	outputFn := svc.makeTerminalOutputFn(terminalID)
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: terminalID, Shell: testutil.TestShell(), WorkingDir: workingDir,
		Cols: 80, Rows: 24,
	}, outputFn, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, dbUpsertTerminalParams(terminalID, workingDir)))
}

func dbUpsertTerminalParams(id, workingDir string) db.UpsertTerminalParams {
	return db.UpsertTerminalParams{
		ID: id, WorkingDir: workingDir, HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte{},
	}
}

func registerTerminalNotifyWatch(t *testing.T, svc *Service, terminalID string) *testResponseWriter {
	t.Helper()
	w := newTestWriter()
	registerTerminalWatch(svc, "ch-signals", terminalID, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, w)
	return w
}

func countTerminalEvents(w *testResponseWriter, terminalID string, pick func(*leapmuxv1.TerminalEvent) bool) int {
	n := 0
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if pick(te) {
			n++
		}
	}
	return n
}

func TestTerminalOutput_BellBroadcastOnLiveWrite(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-bell-live"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x07")))

	assert.Equal(t, 1, countTerminalEvents(w, terminalID, func(te *leapmuxv1.TerminalEvent) bool {
		return te.GetBell() != nil
	}), "live BEL write must broadcast exactly one TerminalBell")
}

func TestTerminalOutput_SnapshotReplayNeverBroadcastsBell(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-bell-replay"
	startTerminalWithOutputFn(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x07")))

	w := newTestWriter()
	sink := newReplaySink(w)
	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	svc.replayTerminalCatchUp(sink, terminalID, 0, row)

	data, _, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
	require.NotEmpty(t, data, "snapshot must include the BEL byte in terminal data")

	assert.Equal(t, 0, countTerminalEvents(w, terminalID, func(te *leapmuxv1.TerminalEvent) bool {
		return te.GetBell() != nil
	}), "catch-up replay must not broadcast bell events")
}

func TestTerminalOutput_BellCoalesceWindow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-bell-coalesce"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x07")))
	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x07")))

	time.Sleep(bellCoalesceWindow + 50*time.Millisecond)
	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x07")))

	assert.Equal(t, 2, countTerminalEvents(w, terminalID, func(te *leapmuxv1.TerminalEvent) bool {
		return te.GetBell() != nil
	}), "two bells inside the coalesce window should broadcast once; one after should broadcast again")
}

func TestTerminalOutput_TitleChangedBroadcastWithoutClobberingRename(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-title"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	// A user rename populates Meta.Title (the field the rename path and
	// ListTerminals hydration surface, and the field persistTerminalOnExit
	// writes to the DB title column on shell exit).
	require.True(t, svc.Terminals.UpdateTitle(terminalID, "user-renamed"))

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x1b]0;new-pty-title\x07")))

	var titles []string
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if tc := te.GetTitleChanged(); tc != nil {
			titles = append(titles, tc.GetTitle())
		}
	}
	require.NotEmpty(t, titles)
	assert.Equal(t, "new-pty-title", titles[len(titles)-1],
		"OSC title must broadcast a TerminalTitleChanged so the live UI overlays it as ptyTitle")

	// The OSC title must NOT write Meta.Title — that field is owned by the
	// user-rename path, and ListTerminals maps it into the client's tab.title,
	// which tabDisplayLabel prefers over the live ptyTitle. Writing it here
	// would silently clobber the rename on the next hydration.
	meta, ok := svc.Terminals.GetMeta(terminalID)
	require.True(t, ok)
	assert.Equal(t, "user-renamed", meta.Title,
		"OSC title must not overwrite Meta.Title (the rename field)")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.Empty(t, row.Title, "OSC title must not write the DB Title column (user rename owns it)")
}

func TestTerminalOutput_NotifyOnlySkipsTerminalData(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-notify-skip-data"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("hello\x07")))

	assert.Equal(t, 0, countTerminalEvents(w, terminalID, func(te *leapmuxv1.TerminalEvent) bool {
		return te.GetData() != nil
	}), "NOTIFY-only watchers must not receive TerminalData")
	assert.Equal(t, 1, countTerminalEvents(w, terminalID, func(te *leapmuxv1.TerminalEvent) bool {
		return te.GetBell() != nil
	}), "NOTIFY watchers must still receive TerminalBell")
}

func TestTerminalOutput_NotificationBroadcast(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-notify"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x1b]9;hi there\x07")))

	var bodies []string
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if n := te.GetNotification(); n != nil {
			bodies = append(bodies, n.GetBody())
		}
	}
	assert.Contains(t, strings.Join(bodies, "|"), "hi there")
}

func TestTerminalOutput_ProgressBroadcast(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-progress"
	startTerminalWithOutputFn(t, svc, terminalID)
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("\x1b]9;4;1;42\x07")))

	var states []leapmuxv1.TerminalProgress_State
	var percents []int32
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if p := te.GetProgress(); p != nil {
			states = append(states, p.GetState())
			percents = append(percents, p.GetPercent())
		}
	}
	require.NotEmpty(t, states)
	assert.Equal(t, leapmuxv1.TerminalProgress_STATE_NORMAL, states[len(states)-1])
	assert.Equal(t, int32(42), percents[len(percents)-1])
}

// terminalTitlesBroadcast collects every TitleChanged title the watcher saw.
func terminalTitlesBroadcast(w *testResponseWriter, terminalID string) []string {
	var out []string
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if tc := te.GetTitleChanged(); tc != nil {
			out = append(out, tc.GetTitle())
		}
	}
	return out
}

// terminalNotificationsBroadcast collects every notification the watcher saw.
func terminalNotificationsBroadcast(w *testResponseWriter, terminalID string) []*leapmuxv1.TerminalNotification {
	var out []*leapmuxv1.TerminalNotification
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		te := resp.GetTerminalEvent()
		if te == nil || te.GetTerminalId() != terminalID {
			continue
		}
		if n := te.GetNotification(); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// The OSC title is the ONE writer of a tab label whose bytes a remote process
// chooses. `printf '\e]0;<anything>\a'` sets it, and so does the PS1 of the
// REMOTE host in an `ssh` session, a `cat` of a hostile file, and any command
// an agent runs in that terminal.
//
// validate.CleanName's own doc says "Every writer of a tab title calls this",
// and this writer did not. The browser renders the result as the tab label and
// the tab tooltip, so a right-to-left override reordered what the tab strip
// showed -- the exact attack testdata/title_cleaning_conformance.json says the
// strip prevents -- and an OSC title could reach oscBufCap (2048 bytes), 16
// times the limit every other writer of a tab title obeys.
func TestBroadcastTerminalSignal_CleansTheOSCTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		osc  string
		want string
	}{
		{
			name: "strips a right-to-left override",
			osc:  "\u202etxt.exe",
			want: "txt.exe",
		},
		{
			name: "strips an invisible run that pads the label",
			osc:  strings.Repeat("\u200b", 500) + "me@host",
			want: "me@host",
		},
		{
			name: "folds a run of whitespace to one space",
			osc:  "  me@host:  ~/src  ",
			want: "me@host: ~/src",
		},
		{
			name: "strips a control character",
			osc:  "me@host\x00:~",
			want: "me@host:~",
		},
		{
			name: "keeps the punctuation a prompt actually writes",
			osc:  `me@host: ~/src "work" 100% $HOME c:\tmp`,
			want: `me@host: ~/src "work" 100% $HOME c:\tmp`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			const terminalID = "term-osc-clean"
			w := registerTerminalNotifyWatch(t, svc, terminalID)

			svc.broadcastTerminalSignal(terminalID, terminal.Signal{
				Kind:  terminal.SignalTitle,
				Title: tc.osc,
			})

			assert.Equal(t, []string{tc.want}, terminalTitlesBroadcast(w, terminalID))
		})
	}
}

// The cap every other writer of a tab label obeys. oscBufCap lets an OSC body
// reach ~2046 bytes, and the browser renders the title into a tooltip with no
// line clamp.
func TestBroadcastTerminalSignal_CapsTheOSCTitle(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-cap"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalTitle,
		Title: strings.Repeat("a", 2046),
	})

	titles := terminalTitlesBroadcast(w, terminalID)
	require.Len(t, titles, 1)
	assert.Len(t, titles[0], validate.NameByteLimit)
}

// A title that cleans to nothing is a NO-OP, not a clear. The same answer
// UpdateTerminalTitle gives, and the answer the browser's own
// `if (!value.title) return` already expects -- broadcasting "" would spend a
// message to say nothing.
func TestBroadcastTerminalSignal_DropsAnOSCTitleThatCleansToNothing(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-empty"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	for _, osc := range []string{"", "   ", "\u200b\ufeff\u00ad", "\x00\x01"} {
		svc.broadcastTerminalSignal(terminalID, terminal.Signal{
			Kind:  terminal.SignalTitle,
			Title: osc,
		})
	}

	assert.Empty(t, terminalTitlesBroadcast(w, terminalID),
		"an OSC title that cleans to nothing must broadcast nothing")
}

// The notification arrives from the same untrusted PTY bytes, and the browser
// hands both fields to the OS notification service.
//
// The TITLE takes the name rule, because it IS a title. The BODY takes the
// strip and the cap WITHOUT the fold and the trim: a run of spaces inside a
// build summary is the sender's formatting.
func TestBroadcastTerminalSignal_CleansTheOSCNotification(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-notify"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalNotification,
		Title: "\u202eBuild   done",
		Body:  "  3 tests   failed\u202e\u200b " + strings.Repeat("x", 1000),
	})

	got := terminalNotificationsBroadcast(w, terminalID)
	require.Len(t, got, 1)
	assert.Equal(t, "Build done", got[0].GetTitle(),
		"the title takes the name rule: the override goes and the whitespace run folds")

	body := got[0].GetBody()
	assert.NotContains(t, body, "\u202e", "the body loses the bidi override")
	assert.NotContains(t, body, "\u200b", "the body loses the invisible run")
	assert.True(t, strings.HasPrefix(body, "  3 tests   failed"),
		"the body keeps the sender's whitespace: no fold and no trim, got %q", body)
	assert.LessOrEqual(t, len(body), notificationBodyByteLimit,
		"the body is capped, because the process that wrote the OSC chose its length")
}

// The two rules answer a NEWLINE differently, and each answer is deliberate.
// The tab title FOLDS it to a space, because a tab label is one line and the
// two words must not run together. The notification body KEEPS it, because a
// body is prose the process wrote and both macOS and the freedesktop spec
// render a body newline as a line break -- deleting it glued "failed" and
// "3 errors" into one word.
//
// Neither DELETES it. A reader sees the space a line break makes, so it is not
// what "unreadable" describes; StripUnreadable asks the whitespace question
// before the control question for exactly this reason.
func TestBroadcastTerminalSignal_HandlesANewlineInEachField(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-newline"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalTitle,
		Title: "me@host\n~/src",
	})
	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalNotification,
		Title: "Build",
		Body:  "one\ntwo",
	})

	assert.Equal(t, []string{"me@host ~/src"}, terminalTitlesBroadcast(w, terminalID),
		"the tab title FOLDS a newline, so the two words do not run together")

	notes := terminalNotificationsBroadcast(w, terminalID)
	require.Len(t, notes, 1)
	assert.Equal(t, "one\ntwo", notes[0].GetBody(),
		"the body KEEPS a newline, which the OS renders as a line break")
}

// The word glue this rule exists to stop, in the shape a build script writes.
func TestBroadcastTerminalSignalKeepsTheLineBreakInABody(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-multiline-body"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	// `printf '\e]777;notify;Build;failed\n3 errors\e\\'`
	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalNotification,
		Title: "Build",
		Body:  "failed\n3 errors",
	})

	notes := terminalNotificationsBroadcast(w, terminalID)
	require.Len(t, notes, 1)
	assert.Equal(t, "failed\n3 errors", notes[0].GetBody(),
		"deleting the newline read as one word: `failed3 errors`")

	// A NON-whitespace control still goes, so an escape sequence and a
	// bidirectional override cannot reach the notification.
	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind:  terminal.SignalNotification,
		Title: "Build",
		Body:  "safe\x1b[31m\u202ereversed",
	})
	notes = terminalNotificationsBroadcast(w, terminalID)
	require.Len(t, notes, 2)
	assert.Equal(t, "safe[31mreversed", notes[1].GetBody(),
		"the ESC and the right-to-left override are removed")
}

// A notification that carries no text at all is dropped, not broadcast.
//
// The OSC 9 parser emits an empty title for a bare `ESC ] 9 ; ST` with no
// cleaning involved, so the guard tests both fields AFTER the clean rather than
// testing whether the clean emptied them -- a "did the clean empty it?" test
// would refuse the invisible-character form and admit the empty one.
func TestBroadcastTerminalSignalDropsATextFreeNotification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal terminal.Signal
	}{
		{"both fields absent", terminal.Signal{Kind: terminal.SignalNotification}},
		{"both clean to nothing", terminal.Signal{
			Kind:  terminal.SignalNotification,
			Title: "\u200b\u202e",
			Body:  "\x00\u200b",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			const terminalID = "term-osc-empty"
			w := registerTerminalNotifyWatch(t, svc, terminalID)

			svc.broadcastTerminalSignal(terminalID, tc.signal)

			assert.Empty(t, terminalNotificationsBroadcast(w, terminalID),
				"a notification with no text says only what the bell says")
		})
	}
}

// A notification with a BODY and no title still reaches the browser: the
// browser supplies "Terminal" for the missing title, and the body is the part
// the reader acts on.
func TestBroadcastTerminalSignalKeepsABodyWithoutATitle(t *testing.T) {
	svc, _, _ := setupTestService(t)
	const terminalID = "term-osc-body-only"
	w := registerTerminalNotifyWatch(t, svc, terminalID)

	svc.broadcastTerminalSignal(terminalID, terminal.Signal{
		Kind: terminal.SignalNotification,
		Body: "3 tests failed",
	})

	got := terminalNotificationsBroadcast(w, terminalID)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].GetTitle())
	assert.Equal(t, "3 tests failed", got[0].GetBody())
}
