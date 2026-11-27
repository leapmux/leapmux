package cmd

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type terminalPSCrossWorker struct {
	call func(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error)
}

func (w *terminalPSCrossWorker) CallInner(ctx context.Context, target string, userID userid.UserID, req *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return w.call(ctx, target, userID, req)
}

func (*terminalPSCrossWorker) StreamInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest, func(*leapmuxv1.InnerStreamMessage) error, func(channel.StreamController)) error {
	return connect.NewError(connect.CodeUnimplemented, "unexpected stream")
}

// shellsDispatcher answers the one worker-local inner RPC
// `terminal shells` makes and records every method it was asked for,
// so a test can prove the command actually reached the worker rather
// than short-circuiting somewhere in resolve/preflight.
type shellsDispatcher struct {
	mu      sync.Mutex
	methods []string
}

func (d *shellsDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	writeProtoResponse(w, &leapmuxv1.ListAvailableShellsResponse{
		Shells:       []string{"/bin/zsh", "/bin/bash"},
		DefaultShell: "/bin/zsh",
	})
}

func (d *shellsDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

// terminalWorker serves one terminal record and records requests that mutate it.
// mu guards every field.
type terminalWorker struct {
	mu       sync.Mutex
	info     *leapmuxv1.TerminalInfo
	inputs   []*leapmuxv1.SendInputRequest
	resizes  []*leapmuxv1.ResizeTerminalRequest
	restarts []*leapmuxv1.RestartTerminalRequest
	// refuseInput, when not empty, is the InvalidArgument message that
	// SendInput answers instead.
	refuseInput string
}

func (w *terminalWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() == leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure {
		var in leapmuxv1.WatchEventsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		sendWatchFrame(rw, &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
			UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: in.GetUpdateId()},
		}})
		<-ctx.Done()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
		var in leapmuxv1.ListTerminalsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		resp := &leapmuxv1.ListTerminalsResponse{}
		if w.info != nil && slices.Contains(in.GetTabIds(), w.info.GetTerminalId()) {
			resp.Terminals = []*leapmuxv1.TerminalInfo{w.info}
		}
		writeProtoResponse(rw, resp)
	case leapmuxv1connect.WorkerTerminalServiceSendInputProcedure:
		var in leapmuxv1.SendInputRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.inputs = append(w.inputs, &in)
		if w.refuseInput != "" {
			_ = rw.SendError(int32(codes.InvalidArgument), w.refuseInput)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.SendInputResponse{})
	case leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure:
		var in leapmuxv1.ResizeTerminalRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.resizes = append(w.resizes, &in)
		writeProtoResponse(rw, &leapmuxv1.ResizeTerminalResponse{})
	case leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure:
		var in leapmuxv1.RestartTerminalRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.restarts = append(w.restarts, &in)
		writeProtoResponse(rw, &leapmuxv1.RestartTerminalResponse{})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestRunTerminalRestartAndResizeSendTheObservedTarget(t *testing.T) {
	const terminalID = "terminal-for-restart"
	const shellID = "shell-instance"
	w := &terminalWorker{info: &leapmuxv1.TerminalInfo{
		TerminalId: terminalID, ShellInstanceId: shellID, Cols: 170, Rows: 49,
	}}
	startTerminalWorker(t, w, terminalID)

	_ = runControl(t, "terminal", "resize", terminalID, "165", "47").Data(t)
	_ = runControl(t, "terminal", "restart", terminalID).Data(t)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", terminalID)
	refusal := runControl(t, "terminal", "restart", terminalID, "--replace").requireFailure(t, cli.CodeSelfTargetRefused)
	assert.Contains(t, refusal.Message, terminalID)
	_ = runControlIn(t, "printf before\nprintf after\n",
		"terminal", "restart", terminalID, "--replace", "--allow-self", "--command", "-", "--json").Data(t)

	w.mu.Lock()
	w.info.ShellInstanceId = ""
	w.mu.Unlock()
	runControl(t, "terminal", "restart", terminalID, "--replace", "--allow-self").requireFailure(t, cli.CodeConflict)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.resizes, 1)
	assert.True(t, proto.Equal(&leapmuxv1.ResizeTerminalRequest{
		TerminalId: terminalID, Cols: 165, Rows: 47,
	}, w.resizes[0]))
	require.Len(t, w.restarts, 2, "the refusals send no restart request")
	assert.True(t, proto.Equal(&leapmuxv1.RestartTerminalRequest{
		TerminalId: terminalID, Cols: 170, Rows: 49,
	}, w.restarts[0]))
	assert.True(t, proto.Equal(&leapmuxv1.RestartTerminalRequest{
		TerminalId: terminalID, Cols: 170, Rows: 49, ReplaceShellInstanceId: shellID, InitialCommand: "printf before\nprintf after",
	}, w.restarts[1]))
}

// recordedInputs returns the SendInput requests that reached the Worker.
func (w *terminalWorker) recordedInputs() []*leapmuxv1.SendInputRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.SendInputRequest(nil), w.inputs...)
}

// startTerminalWorker serves w as the Worker of the terminal terminalID.
func startTerminalWorker(t *testing.T, w *terminalWorker, terminalID string) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:      &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
			TileId:   "root-1",
			WorkerId: "worker-A", WorkspaceId: "ws-1",
		},
	}, w)
}

// TestRunTerminalShells_InsideWorkerSpawn is the end-to-end regression
// test for a worker-scoped command run from inside a worker-spawned
// agent. The spawn injects LEAPMUX_CONTROL_WORKER_ID into every agent
// process (controlenv.WorkerIDVariable), which workerScope picks up as the
// default --worker -- so this is the default state of every
// `leapmux` invocation an agent makes, not an exotic one.
//
// Both hub round-trips that used to hang off that value target
// WorkerManagementService: the resolver's GetWorker existence leg
// (deleted) and maybePreflightWorker's GetWorker (now tolerates a
// lookup it cannot perform). Either one aborted the command with
// `resolve_failed` / `preflight_failed` before it could issue the worker
// RPC it actually wanted, and the hub stub here refuses both -- the
// narrowest grant a delegation bearer could carry.
func TestRunTerminalShells_InsideWorkerSpawn(t *testing.T) {
	disp := &shellsDispatcher{}
	hub := &recordingHub{}
	startSpawnIPC(t, hub, disp)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	data := runControl(t, "terminal", "shells").Data(t)
	assert.Equal(t, "/bin/zsh", data["default_shell"])
	assert.Equal(t, []any{"/bin/zsh", "/bin/bash"}, data["shells"])

	assert.Equal(t, []string{leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure}, disp.called(),
		"the command must reach the worker instead of dying on a denied hub round-trip")
	// The preflight round-trip survives (it is the friendly
	// "no such worker id" check for laptop users); only its failure
	// handling changed. The resolver must add none of its own.
	assert.Equal(t, []string{leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure}, hub.called(),
		"no hub call may be made on the worker axis beyond the tolerated preflight")
}

// TestRunTerminalShells_PreflightStillRejectsUnknownWorker is the
// other half of the pair above: making the preflight tolerate a
// lookup it cannot perform must not make it tolerate an answer it
// can. When GetWorker does answer, its NotFound for the id is still a
// hard `not_found`.
func TestRunTerminalShells_PreflightStillRejectsUnknownWorker(t *testing.T) {
	disp := &shellsDispatcher{}
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, disp)

	// A full id costs no list call, so the preflight is what refuses it.
	ghost := fullTestID("worker-ghost")
	o := runControl(t, "terminal", "shells", "--worker", ghost)
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	f := o.requireFailure(t, "not_found")
	assert.Equal(t, "no such worker: "+ghost, f.Message)
	assert.Empty(t, disp.called(), "a rejected worker id must not reach the worker")
}

// TestTerminalRowOf_StatusIsTheOnlyLifecycleKey pins that `status` alone
// states the lifecycle of a terminal. A second key derived from it says less
// than `status` does: a STARTUP_FAILED terminal runs no process, and such a key
// read false for it. The status prints as its token, and an unset status prints
// the empty token.
func TestTerminalRowOf_StatusIsTheOnlyLifecycleKey(t *testing.T) {
	for status, name := range map[leapmuxv1.TerminalStatus]string{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:         "exited",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY:          "ready",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:       "starting",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED: "startup_failed",
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED:    "",
	} {
		raw, err := json.Marshal(terminalRowOf(&leapmuxv1.TerminalInfo{TerminalId: "term-1", Status: status}))
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.NotContainsf(t, got, "exited", "status %s", status)
		assert.Equalf(t, name, got["status"], "status %s", status)
	}
}

// A terminal whose shell ended states the exit code, 0 included. A record
// without the field states none, and the result then has no exit_code key.
func TestTerminalRowOf_StatesTheExitCode(t *testing.T) {
	for name, tc := range map[string]struct {
		code *int32
		want any
	}{
		"a code":                              {int32Ptr(3), float64(3)},
		"a zero code":                         {int32Ptr(0), float64(0)},
		"a shell that a signal ended":         {int32Ptr(-1), float64(-1)},
		"a shell that runs, or an old Worker": {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			info := &leapmuxv1.TerminalInfo{TerminalId: "term-1", Status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, ExitCode: tc.code}
			got := jsonMap(t, terminalRowOf(info))
			code, present := got["exit_code"]
			assert.Equal(t, tc.want != nil, present)
			assert.Equal(t, tc.want, code)
		})
	}

	// The row keeps a copy, so a later change of the record changes nothing.
	info := &leapmuxv1.TerminalInfo{ExitCode: int32Ptr(3)}
	row := terminalRowOf(info)
	*info.ExitCode = 9
	assert.Equal(t, int32Ptr(3), row.ExitCode)
	assert.Nil(t, exitCodeOf(nil))
}

// `terminal get` prints the exit code that the Worker reports.
func TestTerminalGetOverIPC_PrintsTheExitCode(t *testing.T) {
	id := fullTestID("term-1")
	w := &terminalWorker{info: &leapmuxv1.TerminalInfo{TerminalId: id, Cols: 80, Rows: 24, Status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, ExitCode: int32Ptr(2)}}
	startTerminalWorker(t, w, id)

	data := runControl(t, "terminal", "get", id).Data(t)
	assert.Equal(t, "exited", data["status"])
	assert.Equal(t, float64(2), data["exit_code"])
}

func TestRunTerminalRestart_RefusesQuakeSelfTarget(t *testing.T) {
	const quakeID = "quake-self-term"
	w := &terminalWorker{info: &leapmuxv1.TerminalInfo{
		TerminalId: quakeID, ShellInstanceId: "sh-1", Cols: 80, Rows: 24, Quake: true,
	}}
	startTerminalWorker(t, w, quakeID)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "other-tab")
	t.Setenv("LEAPMUX_CONTROL_TERMINAL_ID", quakeID)

	refusal := runControl(t, "terminal", "restart", quakeID, "--replace").requireFailure(t, cli.CodeSelfTargetRefused)
	assert.Contains(t, refusal.Message, quakeID)

	w.mu.Lock()
	assert.Empty(t, w.restarts, "refused self-replace inside quake terminal must not call RestartTerminal")
	w.mu.Unlock()
}

type emptyShellsDispatcher struct{}

func (*emptyShellsDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	writeProtoResponse(w, &leapmuxv1.ListAvailableShellsResponse{})
}

func TestRunTerminalShells_EmptyEncodesJSONArray(t *testing.T) {
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, &emptyShellsDispatcher{})
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	o := runControl(t, "terminal", "shells")
	data := o.Data(t)
	shells, ok := data["shells"].([]any)
	require.True(t, ok, "shells must encode as JSON array, not null: %s", o.Stdout)
	assert.Empty(t, shells)
}

func TestRunTerminalResize_PreflightsUnknownWorker(t *testing.T) {
	id := fullTestID("term-ghost-wk")
	ghostWorker := fullTestID("worker-ghost")
	w := &terminalWorker{info: &leapmuxv1.TerminalInfo{TerminalId: id, Cols: 80, Rows: 24}}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: id},
			WorkerId:    ghostWorker,
			WorkspaceId: "ws-1",
		},
	}, w)

	f := runControl(t, "terminal", "resize", id, "100", "40").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, ghostWorker)
	w.mu.Lock()
	assert.Empty(t, w.resizes, "rejected worker must not receive ResizeTerminal")
	w.mu.Unlock()
}

func TestTerminalOpen_RejectsAfterOffsetFlag(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "terminal", "open", "--wait", "--change", "--after-offset", "10", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "after-offset")
}

func TestTerminalAttach_RejectsStructuredOutputFlags(t *testing.T) {
	clearRemoteEnv(t)
	for _, flagArgs := range [][]string{
		{"--json"},
		{"--format", "ndjson"},
		{"--format", "tsv"},
		{"--fields", "id"},
		{"--quiet"},
	} {
		t.Run(strings.Join(flagArgs, " "), func(t *testing.T) {
			args := append([]string{"terminal", "attach", fullTestID("term-1"), "--hub", "https://stub.invalid"}, flagArgs...)
			o := runControlText(t, args...)
			assert.Equal(t, cli.ExitUsage, o.Code, "stderr: %s", o.Stderr)
		})
	}
}

func TestTerminalAttach_ExitedAndStartupFailedShortCircuit(t *testing.T) {
	t.Run("exited terminal prints screen once and exits zero", func(t *testing.T) {
		id := fullTestID("term-exited")
		w := &terminalWorker{info: &leapmuxv1.TerminalInfo{
			TerminalId: id,
			Cols:       80,
			Rows:       24,
			Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
			Screen:     []byte("final output\n"),
		}}
		startTerminalWorker(t, w, id)

		o := runControlText(t, "terminal", "attach", id)
		require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
		assert.Equal(t, "final output\n", o.Stdout)
	})

	t.Run("startup_failed terminal returns error immediately", func(t *testing.T) {
		id := fullTestID("term-failed")
		w := &terminalWorker{info: &leapmuxv1.TerminalInfo{
			TerminalId:   id,
			Cols:         80,
			Rows:         24,
			Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
			StartupError: "shell binary missing",
		}}
		startTerminalWorker(t, w, id)

		o := runControlText(t, "terminal", "attach", id)
		assert.Equal(t, cli.ExitFailure, o.Code)
		assert.Contains(t, o.Stderr, "shell binary missing")
	})
}

func TestTerminalPS_ValidationDeduplicationAndEmptyJSON(t *testing.T) {
	t.Run("rejects --all combined with positional targets", func(t *testing.T) {
		clearRemoteEnv(t)
		f := runControl(t, "terminal", "ps", "--all", fullTestID("term-1"), "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "not both")
	})

	t.Run("deduplicates targets, preflights workers, and returns non-null empty array", func(t *testing.T) {
		t1 := fullTestID("term-ps-1")
		t2 := fullTestID("term-ps-2")
		var (
			mu           sync.Mutex
			requestedIDs [][]string
		)
		disp := funcDispatcher(func(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure {
				_ = rw.SendError(int32(codes.Unimplemented), req.GetMethod())
				return
			}
			var in leapmuxv1.InspectTerminalProcessesRequest
			if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
				_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
				return
			}
			mu.Lock()
			requestedIDs = append(requestedIDs, append([]string(nil), in.GetTerminalIds()...))
			mu.Unlock()
			writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{})
		})
		cross := &terminalPSCrossWorker{
			call: func(_ context.Context, _ string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest) ([]byte, error) {
				if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure {
					return nil, connect.NewError(connect.CodeUnimplemented, req.GetMethod())
				}
				var in leapmuxv1.InspectTerminalProcessesRequest
				if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
					return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
				}
				mu.Lock()
				requestedIDs = append(requestedIDs, append([]string(nil), in.GetTerminalIds()...))
				mu.Unlock()
				return proto.Marshal(&leapmuxv1.InspectTerminalProcessesResponse{})
			},
		}
		hub := &recordingHub{
			workers: workerRows("worker-A", "worker-B"),
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t1}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t2}, WorkerId: "worker-B", WorkspaceId: "ws-1"},
			},
		}
		startSpawnIPC(t, hub, disp, func(r *controlipc.Router) { r.CrossWorker = cross })

		// Positional duplicates using prefixes resolve via a single cached ListTabs call.
		o := runControl(t, "terminal", "ps", t1[:12], t1[:12], t2[:12])
		data := o.Data(t)
		procs, ok := data["processes"].([]any)
		require.True(t, ok, "processes must encode as [] not null: %s", o.Stdout)
		assert.Empty(t, procs)

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, requestedIDs, 2, "each worker gets one InspectTerminalProcesses call")
		var allSent []string
		for _, batch := range requestedIDs {
			allSent = append(allSent, batch...)
		}
		slices.Sort(allSent)
		assert.Equal(t, []string{t1, t2}, allSent, "duplicate terminal IDs must be deduplicated")

		listTabsCalls := 0
		for _, m := range hub.called() {
			if m == leapmuxv1connect.WorkspaceServiceListTabsProcedure {
				listTabsCalls++
			}
		}
		assert.Equal(t, 1, listTabsCalls, "positional resolution must reuse one cached ListTabs call")
	})

	t.Run("preflight rejects unknown worker", func(t *testing.T) {
		t1 := fullTestID("term-ps-ghost")
		ghost := fullTestID("worker-ghost")
		var called atomic.Int32
		disp := funcDispatcher(func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			called.Add(1)
			writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{})
		})
		hub := &recordingHub{
			workers: workerRows("worker-A"),
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t1},
				WorkerId:    ghost,
				WorkspaceId: "ws-1",
			},
		}
		startSpawnIPC(t, hub, disp)

		f := runControl(t, "terminal", "ps", t1).requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, ghost)
		assert.Zero(t, called.Load())
	})
}

type funcDispatcher func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter)

func (f funcDispatcher) DispatchWith(ctx context.Context, c channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	f(ctx, c, req, rw)
}

func TestResolveTab_SelfSkipsLocateTabWhenTabTypeAndWorkerIDKnown(t *testing.T) {
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, &creationPublicationDispatcher{})
	tabID := fullTestID("self-term")
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", tabID)
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "terminal")
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	s, err := openSession(&cli.Invocation{Context: t.Context(), Command: &cobra.Command{}})
	require.NoError(t, err)
	defer s.Close()

	got, err := s.resolveTab(resolve.KindTab, resolve.SelfTarget, resolve.Need{
		WorkerID: true,
		Want:     resolve.Wants{TabType: true},
	})
	require.NoError(t, err)
	assert.Equal(t, tabID, got.ID)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, got.Type)
	assert.Equal(t, "worker-A", got.WorkerID)
	assert.True(t, got.Self)
	assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
		"resolveTab must pass Target.TabType and WorkerID so LocateTab is skipped on self")
}

func TestQuakeOpen_MissingDirReturnsUsageError(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "")
	f := runControl(t, "terminal", "quake", "open", "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--dir")
}

func TestTerminalPS_AllWorkspaceDefaultsTruncationAndQuiet(t *testing.T) {
	t.Run("--all defaults empty process names, reports truncated_extra, and formats quiet lines", func(t *testing.T) {
		t1 := fullTestID("term-ps-all-1")
		t2 := fullTestID("term-ps-all-2")
		var (
			mu           sync.Mutex
			requestedIDs []string
		)
		disp := funcDispatcher(func(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure {
				_ = rw.SendError(int32(codes.Unimplemented), req.GetMethod())
				return
			}
			var in leapmuxv1.InspectTerminalProcessesRequest
			if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
				_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
				return
			}
			mu.Lock()
			requestedIDs = append([]string(nil), in.GetTerminalIds()...)
			mu.Unlock()
			writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{
				Terminals: []*leapmuxv1.TerminalProcesses{
					{
						TerminalId: t1,
						Processes: []*leapmuxv1.TerminalProcess{
							{Pid: 101, Name: ""},
							{Pid: 202, Name: "go"},
						},
						TotalCount: 5,
					},
					{
						TerminalId: t2,
						Processes: []*leapmuxv1.TerminalProcess{
							{Pid: 303, Name: "zsh"},
						},
						TotalCount: 1,
					},
				},
			})
		})
		hub := &recordingHub{
			workers: workerRows("worker-A"),
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t1}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t2}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		startSpawnIPC(t, hub, disp)

		data := runControl(t, "terminal", "ps", "--all").Data(t)
		mu.Lock()
		gotIDs := append([]string(nil), requestedIDs...)
		mu.Unlock()
		assert.Equal(t, []string{t1, t2}, gotIDs)

		procs, ok := data["processes"].([]any)
		require.True(t, ok)
		require.Len(t, procs, 3)
		assert.Equal(t, map[string]any{"terminal_id": t1, "pid": float64(101), "name": "(unknown)"}, procs[0])
		assert.Equal(t, map[string]any{"terminal_id": t1, "pid": float64(202), "name": "go"}, procs[1])
		assert.Equal(t, map[string]any{"terminal_id": t2, "pid": float64(303), "name": "zsh"}, procs[2])

		trunc, ok := data["truncated_extra"].(map[string]any)
		require.True(t, ok, "truncated_extra must be present when TotalCount > len(Processes)")
		assert.Equal(t, map[string]any{t1: float64(3)}, trunc)

		quiet := runControlText(t, "terminal", "ps", "--all", "-q")
		require.Equal(t, cli.ExitOK, quiet.Code, "stderr: %s", quiet.Stderr)
		wantLines := []string{
			t1 + "\t101\t(unknown)",
			t1 + "\t202\tgo",
			t2 + "\t303\tzsh",
		}
		assert.Equal(t, strings.Join(wantLines, "\n")+"\n", quiet.Stdout)
		assert.Equal(t, wantLines, (terminalPSResult{
			Processes: []terminalProcessRow{
				{TerminalID: t1, PID: 101, Name: "(unknown)"},
				{TerminalID: t1, PID: 202, Name: "go"},
				{TerminalID: t2, PID: 303, Name: "zsh"},
			},
		}).QuietLines())
	})

	t.Run("--workspace implies --all with no positional targets", func(t *testing.T) {
		t1 := fullTestID("term-ps-ws")
		disp := funcDispatcher(func(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure {
				_ = rw.SendError(int32(codes.Unimplemented), req.GetMethod())
				return
			}
			writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{
				Terminals: []*leapmuxv1.TerminalProcesses{
					{
						TerminalId: t1,
						Processes:  []*leapmuxv1.TerminalProcess{{Pid: 404, Name: "make"}},
						TotalCount: 1,
					},
				},
			})
		})
		hub := &recordingHub{
			workers:    workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", CreatedBy: "u-spawn", Title: "Alpha"}},
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: t1}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "")

		data := runControl(t, "terminal", "ps", "--workspace", "ws-1").Data(t)
		procs, ok := data["processes"].([]any)
		require.True(t, ok)
		assert.Equal(t, []any{map[string]any{"terminal_id": t1, "pid": float64(404), "name": "make"}}, procs)
		assert.NotContains(t, data, "truncated_extra")
	})
}
