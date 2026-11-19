package cmd

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

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

// terminalWorker is a fake Worker that serves one terminal: ListTerminals
// answers its record, and SendInput records each request. mu guards every
// field.
type terminalWorker struct {
	mu     sync.Mutex
	info   *leapmuxv1.TerminalInfo
	inputs []*leapmuxv1.SendInputRequest
	// refuseInput, when not empty, is the InvalidArgument message that
	// SendInput answers instead.
	refuseInput string
}

func (w *terminalWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
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
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
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
// process (controlipc.EnvVars), which BindEntityFlags picks up as the
// --worker-id default -- so this is the default state of every
// `leapmux control` invocation an agent makes, not an exotic one.
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
