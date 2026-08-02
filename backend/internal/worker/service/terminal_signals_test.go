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
