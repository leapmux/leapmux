package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/rpcaccess"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// testWorkerMethod declares a test-only WORKER method that requires one scope.
func testWorkerMethod(procedure string, scope leapmuxv1.Scope, streaming bool) rpcaccess.Method {
	return rpcaccess.Method{
		Procedure:       procedure,
		Host:            leapmuxv1.ServiceHost_SERVICE_HOST_WORKER,
		Access:          &leapmuxv1.MethodAccess{Requirement: &leapmuxv1.MethodAccess_Scope{Scope: scope}},
		ServerStreaming: streaming,
	}
}

// testProcedures declares the procedures the registrar tests register. No
// proto declares them, so a registrar built with newRegistrar would panic on
// each one; testLookup supplies them and falls back to the real declarations.
var testProcedures = map[string]rpcaccess.Method{
	"ProbeAgent":         testWorkerMethod("ProbeAgent", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"ProbeAgentID":       testWorkerMethod("ProbeAgentID", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"ProbeTerm":          testWorkerMethod("ProbeTerm", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"ProbeRestart":       testWorkerMethod("ProbeRestart", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"slow-probe":         testWorkerMethod("slow-probe", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"Dup":                testWorkerMethod("Dup", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"ProbeInvalid":       testWorkerMethod("ProbeInvalid", leapmuxv1.Scope_SCOPE_WORKER_READ, false),
	"ProbeInvalidStream": testWorkerMethod("ProbeInvalidStream", leapmuxv1.Scope_SCOPE_WORKER_READ, true),
}

func testLookup(procedure string) (rpcaccess.Method, bool) {
	if m, ok := testProcedures[procedure]; ok {
		return m, true
	}
	return rpcaccess.Lookup(procedure)
}

func newTestRegistrar(d *channel.Dispatcher, svc *Service) registrar {
	return newRegistrarWithLookup(d, svc, testLookup)
}

func TestRegisterAgentGuarded_PassesLoadedRow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	var gotID, gotDir string
	registerAgentGuarded(r, "ProbeAgent",
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.RenameAgentRequest, row db.Agent, sender channel.ResponseWriter) {
			gotID = row.ID
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.RenameAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RenameAgentRequest{AgentId: "agent-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgent", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
	assert.NotEmpty(t, gotDir, "the loaded row is passed through, not just its id")
}

func TestRegisterAgentGuardedByID_PassesDecodedRequest(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	var gotID string
	registerAgentGuardedByID(r, "ProbeAgentID", dispatchPlain,
		func(_ context.Context, _ channel.Caller, req *leapmuxv1.InterruptAgentRequest, sender channel.ResponseWriter) {
			gotID = req.GetAgentId()
			sendProtoResponse(sender, &leapmuxv1.InterruptAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgentID", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
}

func TestRegisterTerminalGuarded_PassesLoadedRow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, "term-1")
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	var gotID, gotDir string
	registerTerminalGuarded(r, "ProbeTerm",
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.RenameTerminalRequest, row db.Terminal, sender channel.ResponseWriter) {
			gotID = row.ID
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.RenameTerminalResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RenameTerminalRequest{TerminalId: "term-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeTerm", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "term-1", gotID)
	assert.NotEmpty(t, gotDir, "the loaded row is passed through, not just its id")
}

func TestRegisterTerminalForRestartGuarded_PassesRow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, "term-1")
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	var gotDir string
	registerTerminalForRestartGuarded(r, "ProbeRestart",
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender channel.ResponseWriter) {
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.RestartTerminalResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RestartTerminalRequest{TerminalId: "term-1", Cols: 80, Rows: 25})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeRestart", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.NotEmpty(t, gotDir, "the narrow restart row is passed through")
}

// TestGuardedTrackedHelpersTrackInFlightDispatches pins that every *Tracked
// registration helper actually routes through Dispatcher.RegisterTracked: a
// dispatch in flight must hold the BindCleanup WaitGroup open until the
// handler returns. A helper that silently registered untracked would let
// Shutdown tear down the DB pool under a running close flow.
func TestGuardedTrackedHelpersTrackInFlightDispatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		seed     func(t *testing.T, svc *Service)
		register func(r registrar, method string, block func())
		req      proto.Message
	}{
		{
			name: "registerAgentGuardedByID+dispatchTracked",
			seed: func(t *testing.T, svc *Service) { seedAgent(t, svc, "agent-1") },
			register: func(r registrar, method string, block func()) {
				registerAgentGuardedByID(r, method, dispatchTracked,
					func(context.Context, channel.Caller, *leapmuxv1.InterruptAgentRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"},
		},
		{
			name: "registerTerminalGuardedByID+dispatchTracked",
			seed: func(t *testing.T, svc *Service) { seedTerminal(t, svc, "term-1") },
			register: func(r registrar, method string, block func()) {
				registerTerminalGuardedByID(r, method, dispatchTracked,
					func(context.Context, channel.Caller, *leapmuxv1.ResizeTerminalRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.ResizeTerminalRequest{TerminalId: "term-1"},
		},
		{
			name: "registerOwnerGuarded+dispatchTracked",
			seed: func(*testing.T, *Service) {},
			register: func(r registrar, method string, block func()) {
				registerOwnerGuarded(r, method, dispatchTracked,
					func(context.Context, channel.Caller, *leapmuxv1.CloseTabRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.CloseTabRequest{Tab: fileTabIdent("tab-1")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			tc.seed(t, svc)
			d := channel.NewDispatcher()
			var wg sync.WaitGroup
			d.BindCleanup(&wg)
			r := newTestRegistrar(d, svc)

			release := make(chan struct{})
			handlerEntered := make(chan struct{})
			tc.register(r, "slow-probe", func() {
				close(handlerEntered)
				<-release
			})

			payload, err := proto.Marshal(tc.req)
			require.NoError(t, err)
			d.DispatchAsync(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
				Method: "slow-probe", Payload: payload,
			}, newTestWriter())

			waitReturned := make(chan struct{})
			go func() {
				wg.Wait()
				close(waitReturned)
			}()

			<-handlerEntered
			select {
			case <-waitReturned:
				t.Fatal("wg.Wait() returned before the handler finished")
			default:
			}

			close(release)
			<-waitReturned
		})
	}
}

func TestRegistrarPanicsOnDuplicateMethod(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	noop := func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {}
	registerUnguarded(r, "Dup", noop)
	assert.Panics(t, func() { registerUnguarded(r, "Dup", noop) }, "the registrar refuses a second registration")
	assert.Panics(t, func() { d.Register("Dup", noop) }, "and so does the dispatcher, for a caller that bypasses the registrar")
}

// A procedure no proto declares cannot be served: the registrar would have no
// scope and no reply shape to apply, so it refuses to start.
func TestRegistrarPanicsOnAnUndeclaredProcedure(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	noop := func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {}
	for _, procedure := range []string{"OpenAgent", "/leapmux.v1.WorkerAgentService/NoSuchMethod", ""} {
		r := newRegistrar(channel.NewDispatcher(), svc)
		assert.Panicsf(t, func() { registerUnguarded(r, procedure, noop) }, "%q", procedure)
	}
}

// A method declared on a hub or IPC service is not the worker's to serve.
func TestRegistrarPanicsOnANonWorkerProcedure(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	noop := func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {}
	for _, procedure := range []string{
		leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		leapmuxv1connect.ControlIPCServiceWhoamiProcedure,
	} {
		r := newRegistrar(channel.NewDispatcher(), svc)
		assert.Panicsf(t, func() { registerUnguarded(r, procedure, noop) }, "%s", procedure)
	}
}

// A declaration stating no grantable scope fails at boot, not in production.
//
// The three non-grantable values are the whole fail-closed construction: the
// zero is "nobody classified this", NEVER is the recorded refusal, and ALL is
// a grant a producer states rather than one a gate defaults to. Wiring a
// handler behind any of them is a category error -- a gate can only test
// membership in a grantable permission -- so the registrar refuses to start
// rather than serve a method whose gate cannot fire. A declaration with no
// requirement selected, or a public one, fails the same way.
func TestRegistrarPanicsOnAnUngrantableScope(t *testing.T) {
	t.Parallel()

	noop := func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {}
	cases := map[string]*leapmuxv1.MethodAccess{
		"absent":         nil,
		"no requirement": {},
		"public":         {Requirement: &leapmuxv1.MethodAccess_Public{Public: &leapmuxv1.PublicAccess{}}},
	}
	for _, scope := range []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_UNSPECIFIED,
		leapmuxv1.Scope_SCOPE_NEVER,
		leapmuxv1.Scope_SCOPE_ALL,
	} {
		cases[scope.String()] = &leapmuxv1.MethodAccess{Requirement: &leapmuxv1.MethodAccess_Scope{Scope: scope}}
	}
	for name, access := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			lookup := func(procedure string) (rpcaccess.Method, bool) {
				if procedure != "Whatever" {
					return rpcaccess.Lookup(procedure)
				}
				return rpcaccess.Method{Procedure: procedure, Host: leapmuxv1.ServiceHost_SERVICE_HOST_WORKER, Access: access}, true
			}
			r := newRegistrarWithLookup(channel.NewDispatcher(), svc, lookup)
			assert.Panics(t, func() { registerUnguarded(r, "Whatever", noop) })
		})
	}
}

// The row-loading guards answer their refusals unary, so registering one for a
// server-streaming method would leave the client waiting on a stream that never
// errors. The registrar refuses that combination at boot.
//
// Each of the five helpers states its own requireUnary call, so each one is
// checked: a helper that dropped the call would pass a single-helper check.
// Each case uses a fresh dispatcher, because the dispatcher refuses a second
// registration of the same procedure, and that panic would pass the case for
// the wrong reason.
func TestRowGuardsRefuseAStreamingMethod(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	cases := map[string]func(r registrar, procedure string){
		"registerAgentGuarded": func(r registrar, procedure string) {
			registerAgentGuarded(r, procedure,
				func(context.Context, channel.Caller, *leapmuxv1.RenameAgentRequest, db.Agent, channel.ResponseWriter) {
				})
		},
		"registerAgentGuardedByID": func(r registrar, procedure string) {
			registerAgentGuardedByID(r, procedure, dispatchPlain,
				func(context.Context, channel.Caller, *leapmuxv1.InterruptAgentRequest, channel.ResponseWriter) {})
		},
		"registerTerminalGuarded": func(r registrar, procedure string) {
			registerTerminalGuarded(r, procedure,
				func(context.Context, channel.Caller, *leapmuxv1.RenameTerminalRequest, db.Terminal, channel.ResponseWriter) {
				})
		},
		"registerTerminalGuardedByID": func(r registrar, procedure string) {
			registerTerminalGuardedByID(r, procedure, dispatchPlain,
				func(context.Context, channel.Caller, *leapmuxv1.ResizeTerminalRequest, channel.ResponseWriter) {})
		},
		"registerTerminalForRestartGuarded": func(r registrar, procedure string) {
			registerTerminalForRestartGuarded(r, procedure,
				func(context.Context, channel.Caller, *leapmuxv1.RestartTerminalRequest, db.GetTerminalForRestartRow, channel.ResponseWriter) {
				})
		},
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			d := channel.NewDispatcher()
			assert.Panics(t, func() { register(newTestRegistrar(d, svc), "ProbeInvalidStream") },
				"a row guard must refuse a server-streaming method")
			assert.Empty(t, d.Methods(), "the refused registration must wire nothing")

			// The same helper accepts a unary method, so the panic above comes
			// from the shape and not from something else in the helper.
			unary := channel.NewDispatcher()
			assert.NotPanics(t, func() { register(newTestRegistrar(unary, svc), "ProbeInvalid") })
			assert.Equal(t, []string{"ProbeInvalid"}, unary.Methods())
		})
	}
}

// TestRegisterOwnerGuarded_InvalidPayloadAnswersInvalidArgument restores coverage
// deleted with the workspace gate.
//
// `decodeInto` still implements the rule for every owner-gated method -- a
// payload that fails to unmarshal answers INVALID_ARGUMENT and the handler is
// never entered -- but nothing exercised it any more: every other dispatch in
// this package passes a `proto.Marshal`ed payload, so removing the guard, or
// forwarding the failed decode to the handler, would compile and pass the whole
// suite. At runtime a garbled request would then run the handler against a
// ZERO-VALUED message. That is not always harmless: ListAgents / ListTerminals /
// CleanupWorkspace have no empty-field guard of their own and would answer a
// SUCCESSFUL empty response, so the caller sees "no agents" rather than an error.
func TestRegisterOwnerGuarded_InvalidPayloadAnswersInvalidArgument(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	called := false
	registerOwnerGuarded(r, "ProbeInvalid", dispatchPlain,
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.ListAgentsRequest, _ channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeInvalid", Payload: []byte("not-a-proto"),
	}, w)

	require.Len(t, w.errors, 1, "a malformed payload must be refused")
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Equal(t, "invalid request", w.errors[0].message)
	assert.False(t, called, "the handler must never see a request that failed to decode")
	assert.Empty(t, w.responses, "and no response may be sent")
}

// TestRegisterOwnerGuardedStream_InvalidPayloadAnswersStreamError is the streaming
// half, and the shape matters as much as the code: a streaming method's failures
// must arrive as stream frames so the receiver has an End to terminate on. The
// same helper serves both shapes; the declaration decides which one it answers
// in.
//
// Asserts `w.errors` is EMPTY rather than going through `rejections()`, which
// folds both shapes together and would pass either way -- the fold is exactly
// what let this distinction go unchecked.
func TestRegisterOwnerGuardedStream_InvalidPayloadAnswersStreamError(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newTestRegistrar(d, svc)

	called := false
	registerOwnerGuarded(r, "ProbeInvalidStream", dispatchPlain,
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.WatchEventsRequest, _ channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeInvalidStream", Payload: []byte("not-a-proto"),
	}, w)

	assert.Empty(t, w.errors, "a streaming method must not answer with a unary error frame")
	frames := w.streamsSnapshot()
	require.Len(t, frames, 1, "the refusal must arrive as exactly one stream frame")
	assert.NotNil(t, frames[0].GetError())
	assert.True(t, frames[0].GetEnd(), "and be terminal")
	assert.Equal(t, int32(codeInvalidArgument), frames[0].GetError().GetCode())
	assert.Equal(t, "invalid request", frames[0].GetError().GetMessage())
	assert.False(t, called, "the handler must never see a request that failed to decode")
}
