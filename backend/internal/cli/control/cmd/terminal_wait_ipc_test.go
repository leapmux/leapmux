package cmd

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// watchedTerminalWorker is a fake Worker for `terminal wait` over the local IPC
// socket. It answers each read of the terminal from records, acknowledges
// each watch and keeps its stream open, so a test can push the events of
// the terminal. mu guards every field.
type watchedTerminalWorker struct {
	mu sync.Mutex
	// records answers the reads of the terminal in order. The last one
	// answers every read after it.
	records []*leapmuxv1.TerminalInfo
	reads   int
	// watches holds the request of each watch, in order.
	watches []*leapmuxv1.WatchEventsRequest
	// streams holds the frames that each open watch still has to send.
	streams []chan *leapmuxv1.WatchEventsResponse
	methods []string
	// watched receives the request of each watch that the Worker
	// acknowledged.
	watched chan *leapmuxv1.WatchEventsRequest
}

func newWatchedTerminalWorker(records ...*leapmuxv1.TerminalInfo) *watchedTerminalWorker {
	return &watchedTerminalWorker{records: records, watched: make(chan *leapmuxv1.WatchEventsRequest, 8)}
}

func (w *watchedTerminalWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	w.methods = append(w.methods, req.GetMethod())
	w.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
		w.mu.Lock()
		record := w.records[min(w.reads, len(w.records)-1)]
		w.reads++
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.ListTerminalsResponse{Terminals: []*leapmuxv1.TerminalInfo{record}})
	case leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure:
		var in leapmuxv1.WatchEventsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		frames := make(chan *leapmuxv1.WatchEventsResponse, 16)
		w.mu.Lock()
		w.watches = append(w.watches, &in)
		w.streams = append(w.streams, frames)
		w.mu.Unlock()
		sendWatchFrame(rw, &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
			UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: in.GetUpdateId()},
		}})
		w.watched <- &in
		for {
			select {
			case frame := <-frames:
				sendWatchFrame(rw, frame)
			case <-ctx.Done():
				return
			}
		}
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

// push sends te, an event of the terminal id, on every open watch.
func (w *watchedTerminalWorker) push(id string, te *leapmuxv1.TerminalEvent) {
	w.mu.Lock()
	streams := slices.Clone(w.streams)
	w.mu.Unlock()
	for _, frames := range streams {
		frames <- terminalFrame(id, te)
	}
}

func (w *watchedTerminalWorker) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.methods)
}

// startWatchedTerminalWorker serves w as the Worker of the terminal terminalID,
// which the hub places on it.
func startWatchedTerminalWorker(t *testing.T, w *watchedTerminalWorker, terminalID string) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
			TileId:      "root-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}, w)
}

// terminalRecord is the record of the terminal id: a shell in status, at 80
// by 24, whose retained bytes are screen.
func terminalRecord(id string, status leapmuxv1.TerminalStatus, screen string) *leapmuxv1.TerminalInfo {
	return &leapmuxv1.TerminalInfo{
		TerminalId: id, Cols: 80, Rows: 24, Screen: []byte(screen),
		ScreenEndOffset: int64(len(screen)), Status: status,
	}
}

func TestTerminalWaitExitOverIPC_PrintsTheExitCode(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		req := receive(t, ctx, w.watched)
		assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, req.GetTerminals()[0].GetMode(), "a wait for exit reads no bytes")
		w.push(id, closedEvent(3))
	}()

	o := runControlText(t, "terminal", "wait", id, "--exit")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "3\n", o.Stdout, "the text form prints the exit code alone")
}

func TestTerminalWaitExitOverIPC_PrintsTheResultAsJSON(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.watched)
		w.push(id, closedEvent(0))
	}()

	data := runControl(t, "terminal", "wait", id, "--exit").Data(t)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": true, "exit_code": float64(0)}, data)
}

// exitedRecord is the record of the terminal id whose shell ended with code,
// as a Worker that reports TerminalInfo.exit_code states it.
func exitedRecord(id string, code int32) *leapmuxv1.TerminalInfo {
	record := terminalRecord(id, terminalExited, "$ exit\r\n")
	record.ExitCode = &code
	return record
}

// A shell that ended before the wait began ends the wait at once, with the
// exit code that the Worker kept. The wait watches nothing.
func TestTerminalWaitExitOverIPC_PrintsTheCodeOfAShellThatAlreadyEnded(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(exitedRecord(id, 3))
	startWatchedTerminalWorker(t, w, id)

	o := runControlText(t, "terminal", "wait", id, "--exit")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "3\n", o.Stdout)
	assert.Empty(t, o.Stderr, "a known exit code needs no notice")
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)

	data := runControl(t, "terminal", "wait", id, "--exit").Data(t)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": true, "exit_code": float64(3)}, data)
}

// A Worker that holds no record of the exit, because it was killed while the
// shell ran, reports no exit code. The wait still ends at once and exits 0,
// prints no exit code, and says why on stderr.
func TestTerminalWaitExitOverIPC_ReportsNoExitCodeWithoutARecord(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalExited, "$ exit\r\n"))
	startWatchedTerminalWorker(t, w, id)

	o := runControlText(t, "terminal", "wait", id, "--exit")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Empty(t, o.Stdout, "the record states no exit code")
	assert.Contains(t, o.Stderr, "holds no record of the exit code")
	assert.NotContains(t, o.Stderr, "newer", "the reason is the missing record, not a version")
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)
}

// The shell ends after the first read and before the Worker registers the
// watch, so no event reports it. The read after the acknowledgement does,
// with the exit code.
func TestTerminalWaitExitOverIPC_SeesAnExitBeforeTheWatch(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "), exitedRecord(id, 7))
	startWatchedTerminalWorker(t, w, id)

	data := runControl(t, "terminal", "wait", id, "--exit").Data(t)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": true, "exit_code": float64(7)}, data)
}

// A status change to EXITED carries no exit code. The wait reads the record,
// which states it.
func TestTerminalWaitExitOverIPC_ReadsTheCodeAfterAnExitedStatus(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "), terminalRecord(id, terminalReady, "$ "), exitedRecord(id, 5))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.watched)
		w.push(id, statusEvent(terminalExited, ""))
	}()

	o := runControlText(t, "terminal", "wait", id, "--exit")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "5\n", o.Stdout)
	assert.Empty(t, o.Stderr)
}

func TestTerminalWaitExitOverIPC_TimesOutWithExitSix(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)

	o := runControl(t, "terminal", "wait", id, "--exit", "--timeout", "100ms")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	env := o.envelope(t)
	assert.Equal(t, map[string]any{"id": id, "status": "ready", "reached": false}, env.Data, "the result prints whether the condition held or not")
	assert.Contains(t, o.Stderr, "the wait ended after 100ms, before the shell of terminal")
}

func TestTerminalWaitExitOverIPC_RefusesTheCallingTerminal(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	t.Setenv("LEAPMUX_CONTROL_TERMINAL_ID", id)

	for _, target := range []string{id, "self"} {
		f := runControl(t, "terminal", "wait", target, "--exit").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "calling terminal")
	}
	assert.Empty(t, w.called(), "a refused wait reads and watches nothing")
}

// Only a wait for exit refuses the calling terminal. A wait for text in the
// calling terminal can end, because another process in it can print the text.
func TestTerminalWaitTextOverIPC_WaitsInTheCallingTerminal(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "build ok\r\n$ "))
	startWatchedTerminalWorker(t, w, id)
	t.Setenv("LEAPMUX_CONTROL_TERMINAL_ID", id)

	for _, target := range []string{id, "self"} {
		o := runControlText(t, "terminal", "wait", target, "--text", "build ok")
		require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
		assert.Equal(t, "build ok\n", o.Stdout, target)
	}
}

func TestTerminalWaitTextOverIPC_EndsAtOnceWhenTheScreenShowsTheText(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "Listening on :8080\r\n$ "))
	startWatchedTerminalWorker(t, w, id)

	o := runControlText(t, "terminal", "wait", id, "--text", "Listening on")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "Listening on :8080\n", o.Stdout, "the text form prints the line that matched")
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)
}

func TestTerminalWaitTextOverIPC_CanSearchExistingScrollback(t *testing.T) {
	id := fullTestID("term-1")
	const marker = "build completed in the earlier screen"
	info := terminalRecord(id, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, marker+"\r\nclosing\r\nfinished\r\n")
	info.Rows = 2
	w := newWatchedTerminalWorker(info)
	startWatchedTerminalWorker(t, w, id)

	defaultWait := runControl(t, "terminal", "wait", id, "--text", marker)
	assert.Equal(t, cli.ExitWaitEnded, defaultWait.Code)
	assert.Contains(t, defaultWait.Stdout, `"reached":false`)

	withHistory := runControlText(t, "terminal", "wait", id, "--scrollback", "--text", marker)
	require.Equal(t, cli.ExitOK, withHistory.Code, withHistory.Stderr)
	assert.Equal(t, marker+"\n", withHistory.Stdout)
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)

	calls := w.called()
	failure := runControl(t, "terminal", "wait", id, "--exit", "--scrollback").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, failure.Message, "--scrollback needs --text or --regex")
	assert.Equal(t, calls, w.called(), "invalid flags make no new Worker requests")
}

func TestTerminalWaitTextOverIPC_ReportsTheStatusOfAnInitialMatch(t *testing.T) {
	id := fullTestID("term-1")
	info := terminalRecord(id, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, "DONE\r\n")
	info.ExitCode = int32Ptr(0)
	w := newWatchedTerminalWorker(info)
	startWatchedTerminalWorker(t, w, id)
	data := runControl(t, "terminal", "wait", id, "--text", "DONE").Data(t)
	assert.Equal(t, "DONE", data["line"])
	assert.Equal(t, true, data["reached"])
	assert.Equal(t, "exited", data["status"])
	assert.Equal(t, float64(0), data["exit_code"])
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)
}

func TestTerminalWaitTextOverIPC_MatchesOutputThatArrivesInParts(t *testing.T) {
	id := fullTestID("term-1")
	screen := "$ make test\r\n"
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, screen))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		req := receive(t, ctx, w.watched)
		require.Len(t, req.GetTerminals(), 1)
		entry := req.GetTerminals()[0]
		assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, entry.GetMode(), "a wait for text reads the bytes")
		assert.Equal(t, int64(len(screen)), entry.GetAfterOffset(), "the watch resumes where the record ended")
		w.push(id, statusEvent(terminalReady, ""))
		w.push(id, dataEvent("ok  \x1b[32mPA", 30, false))
		w.push(id, dataEvent("SS\x1b[0m pkg\r\n", 45, false))
	}()

	data := runControl(t, "terminal", "wait", id, "--text", "PASS").Data(t)
	assert.Equal(t, map[string]any{"id": id, "status": "ready", "reached": true, "line": "ok  PASS pkg"}, data)
}

func TestTerminalWaitRegexOverIPC_SkipsTheEchoedCommandLine(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.watched)
		w.push(id, dataEvent("make; echo DONE\r\n", 20, false))
		w.push(id, dataEvent("DONE\r\n$ ", 28, false))
	}()

	o := runControlText(t, "terminal", "wait", id, "--regex", "^DONE$")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "DONE\n", o.Stdout)
}

func TestTerminalWaitTextOverIPC_ExitsSixWhenTheShellExitsFirst(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.watched)
		w.push(id, dataEvent("exit\r\n", 8, false))
		w.push(id, closedEvent(130))
	}()

	o := runControl(t, "terminal", "wait", id, "--text", "PASS")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": false, "exit_code": float64(130)}, o.envelope(t).Data)
	assert.Contains(t, o.Stderr, `exited with code 130 before the terminal showed the text "PASS"`)
}

func TestTerminalWaitTextOverIPC_ExitsSixForAShellThatAlreadyEnded(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(exitedRecord(id, 2))
	startWatchedTerminalWorker(t, w, id)

	o := runControl(t, "terminal", "wait", id, "--text", "PASS")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": false, "exit_code": float64(2)}, o.envelope(t).Data)
	assert.Contains(t, o.Stderr, `exited with code 2 before the terminal showed the text "PASS"`)
	assert.NotContains(t, w.called(), leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure)
}

// An older Worker states no exit code, and the notice says only that the
// shell ended.
func TestTerminalWaitTextOverIPC_AnOlderWorkerStatesNoCodeForAShellThatAlreadyEnded(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalExited, "$ exit\r\n"))
	startWatchedTerminalWorker(t, w, id)

	o := runControl(t, "terminal", "wait", id, "--text", "PASS")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, map[string]any{"id": id, "status": "exited", "reached": false}, o.envelope(t).Data)
	assert.Contains(t, o.Stderr, `the shell of terminal `+id+` ended before the terminal showed the text "PASS"`)
}

func TestTerminalWaitTextOverIPC_ExitsSixWhenTheTerminalCloses(t *testing.T) {
	id := fullTestID("term-1")
	w := newWatchedTerminalWorker(terminalRecord(id, terminalReady, "$ "))
	startWatchedTerminalWorker(t, w, id)
	ctx := testutil.DeadlineContext(t)
	go func() {
		req := receive(t, ctx, w.watched)
		w.mu.Lock()
		frames := w.streams[0]
		w.mu.Unlock()
		frames <- &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
			UpdateId:          req.GetUpdateId(),
			RejectedTerminals: []*leapmuxv1.WatchRejection{{EntityId: id, Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}},
		}}}
	}()

	o := runControl(t, "terminal", "wait", id, "--text", "PASS")
	f := o.Failure(t)
	assert.Equal(t, cli.CodeNotFound, f.Code)
	assert.Equal(t, cli.ExitWaitEnded, o.Code)
	assert.Contains(t, f.Message, "closed during the wait")
}
