package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
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

func (d *shellsDispatcher) DispatchWith(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	if req.GetMethod() != "ListAvailableShells" {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	payload, err := proto.Marshal(&leapmuxv1.ListAvailableShellsResponse{
		Shells:       []string{"/bin/zsh", "/bin/bash"},
		DefaultShell: "/bin/zsh",
	})
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload})
}

func (d *shellsDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

// TestRunTerminalShells_InsideWorkerSpawn is the end-to-end regression
// test for a worker-scoped command run from inside a worker-spawned
// agent. The spawn injects LEAPMUX_REMOTE_WORKER_ID into every agent
// process (remoteipc.EnvVars), which BindEntityFlags picks up as the
// --worker-id default -- so this is the default state of every
// `leapmux remote` invocation an agent makes, not an exotic one.
//
// Both hub round-trips that used to hang off that value target
// WorkerManagementService, which is absent from the hub's
// `auth.delegationAllowedProcedures`: the resolver's GetWorker
// existence leg (deleted) and maybePreflightWorker's ListWorkers (now
// tolerates a lookup it cannot perform). Either one aborted the
// command with `resolve_failed` / `preflight_failed` before it could
// issue the worker RPC it actually wanted.
func TestRunTerminalShells_InsideWorkerSpawn(t *testing.T) {
	disp := &shellsDispatcher{}
	hub := &recordingHub{}
	startSpawnIPC(t, hub, disp)
	t.Setenv("LEAPMUX_REMOTE_WORKER_ID", "worker-A")

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunTerminalShells(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error, "terminal shells must succeed inside a worker spawn")
	assert.Equal(t, "/bin/zsh", env.Data["default_shell"])
	assert.Equal(t, []any{"/bin/zsh", "/bin/bash"}, env.Data["shells"])

	assert.Equal(t, []string{"ListAvailableShells"}, disp.called(),
		"the command must reach the worker instead of dying on a denied hub round-trip")
	// The preflight round-trip survives (it is the friendly
	// "no such worker id" check for laptop users); only its failure
	// handling changed. The resolver must add none of its own.
	assert.Equal(t, []string{"ListWorkers"}, hub.called(),
		"no hub call may be made on the --worker-id axis beyond the tolerated preflight")
}

// TestRunTerminalShells_PreflightStillRejectsUnknownWorker is the
// other half of the pair above: making the preflight tolerate a
// lookup it cannot perform must not make it tolerate an answer it
// can. When ListWorkers does come back, an id missing from it is
// still a hard `not_found`.
func TestRunTerminalShells_PreflightStillRejectsUnknownWorker(t *testing.T) {
	disp := &shellsDispatcher{}
	hub := &recordingHub{listWorkers: []string{"worker-A"}}
	startSpawnIPC(t, hub, disp)

	out := withCapturedStdout(t, func() {
		// The handler returns the same envelope it printed, so a
		// non-nil error here is the expected outcome, not a failure.
		require.Error(t, RunTerminalShells(fakeCmdCtx{}, []string{"--worker-id", "worker-ghost"}))
	})

	// Unmarshalling the WHOLE buffer also pins that exactly one
	// envelope was written: a second concatenated object would make
	// this fail with "invalid character '{' after top-level value".
	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.NotNil(t, env.Error)
	assert.Equal(t, "not_found", env.Error["code"])
	assert.Equal(t, "no such worker: worker-ghost", env.Error["message"])
	assert.Empty(t, disp.called(), "a rejected worker id must not reach the worker")
}
