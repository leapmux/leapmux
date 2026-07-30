package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestRegisterAgentGated_PassesLoadedRow(t *testing.T) {
	svc, _, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID, gotDir string
	registerAgentGated(r, "ProbeAgent",
		func(_ context.Context, _ userid.UserID, _ *leapmuxv1.RenameAgentRequest, row db.Agent, sender channel.ResponseWriter) {
			gotID = row.ID
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.RenameAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RenameAgentRequest{AgentId: "agent-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgent", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
	assert.NotEmpty(t, gotDir, "the loaded row is passed through, not just its id")
}

func TestRegisterAgentGatedByID_PassesDecodedRequest(t *testing.T) {
	svc, _, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID string
	registerAgentGatedByID(r, "ProbeAgentID", dispatchPlain,
		func(_ context.Context, _ userid.UserID, req *leapmuxv1.InterruptAgentRequest, sender channel.ResponseWriter) {
			gotID = req.GetAgentId()
			sendProtoResponse(sender, &leapmuxv1.InterruptAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgentID", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
}

func TestRegisterTerminalGated_PassesLoadedRow(t *testing.T) {
	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, "term-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID, gotDir string
	registerTerminalGated(r, "ProbeTerm",
		func(_ context.Context, _ userid.UserID, _ *leapmuxv1.UpdateTerminalTitleRequest, row db.Terminal, sender channel.ResponseWriter) {
			gotID = row.ID
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.UpdateTerminalTitleRequest{TerminalId: "term-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeTerm", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "term-1", gotID)
	assert.NotEmpty(t, gotDir, "the loaded row is passed through, not just its id")
}

func TestRegisterTerminalForRestartGated_PassesRow(t *testing.T) {
	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, "term-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotDir string
	registerTerminalForRestartGated(r, "ProbeRestart",
		func(_ context.Context, _ userid.UserID, _ *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender channel.ResponseWriter) {
			gotDir = row.WorkingDir
			sendProtoResponse(sender, &leapmuxv1.RestartTerminalResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RestartTerminalRequest{TerminalId: "term-1", Cols: 80, Rows: 25})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeRestart", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.NotEmpty(t, gotDir, "the narrow restart row is passed through")
}

// TestGatedTrackedHelpersTrackInFlightDispatches pins that every *Tracked
// registration helper actually routes through Dispatcher.RegisterTracked: a
// dispatch in flight must hold the BindCleanup WaitGroup open until the
// handler returns. A helper that silently registered untracked would let
// Shutdown tear down the DB pool under a running close flow.
func TestGatedTrackedHelpersTrackInFlightDispatches(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(t *testing.T, svc *Service)
		register func(r registrar, method string, block func())
		req      proto.Message
	}{
		{
			name: "registerAgentGatedByID+dispatchTracked",
			seed: func(t *testing.T, svc *Service) { seedAgent(t, svc, "agent-1") },
			register: func(r registrar, method string, block func()) {
				registerAgentGatedByID(r, method, dispatchTracked,
					func(context.Context, userid.UserID, *leapmuxv1.CloseAgentRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"},
		},
		{
			name: "registerTerminalGatedByID+dispatchTracked",
			seed: func(t *testing.T, svc *Service) { seedTerminal(t, svc, "term-1") },
			register: func(r registrar, method string, block func()) {
				registerTerminalGatedByID(r, method, dispatchTracked,
					func(context.Context, userid.UserID, *leapmuxv1.CloseTerminalRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.CloseTerminalRequest{TerminalId: "term-1"},
		},
		{
			name: "registerOwnerGated+dispatchTracked",
			seed: func(*testing.T, *Service) {},
			register: func(r registrar, method string, block func()) {
				registerOwnerGated(r, method, dispatchTracked,
					func(context.Context, userid.UserID, *leapmuxv1.RevokeFileTabPathRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.RevokeFileTabPathRequest{TabId: "tab-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			tc.seed(t, svc)
			d := channel.NewDispatcher()
			var wg sync.WaitGroup
			d.BindCleanup(&wg)
			r := newRegistrar(d, svc)

			release := make(chan struct{})
			handlerEntered := make(chan struct{})
			tc.register(r, "slow-probe", func() {
				close(handlerEntered)
				<-release
			})

			payload, err := proto.Marshal(tc.req)
			require.NoError(t, err)
			d.DispatchAsync(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
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
	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	registerUngated(r, "Dup", func(context.Context, userid.UserID, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {})
	assert.Panics(t, func() {
		registerUngated(r, "Dup", func(context.Context, userid.UserID, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {})
	})
}

// TestRegisterOwnerGated_InvalidPayloadAnswersInvalidArgument restores coverage
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
func TestRegisterOwnerGated_InvalidPayloadAnswersInvalidArgument(t *testing.T) {
	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerOwnerGated(r, "ProbeInvalid", dispatchPlain,
		func(_ context.Context, _ userid.UserID, _ *leapmuxv1.ListAgentsRequest, _ channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeInvalid", Payload: []byte("not-a-proto"),
	}, w)

	require.Len(t, w.errors, 1, "a malformed payload must be refused")
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Equal(t, "invalid request", w.errors[0].message)
	assert.False(t, called, "the handler must never see a request that failed to decode")
	assert.Empty(t, w.responses, "and no response may be sent")
}

// TestRegisterOwnerGatedStream_InvalidPayloadAnswersStreamError is the streaming
// half, and the shape matters as much as the code: a streaming method's failures
// must arrive as stream frames so the receiver has an End to terminate on.
//
// Asserts `w.errors` is EMPTY rather than going through `rejections()`, which
// folds both shapes together and would pass either way -- the fold is exactly
// what let this distinction go unchecked.
func TestRegisterOwnerGatedStream_InvalidPayloadAnswersStreamError(t *testing.T) {
	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerOwnerGatedStream(r, "ProbeInvalidStream",
		func(_ context.Context, _ userid.UserID, _ *leapmuxv1.WatchEventsRequest, _ channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method: "ProbeInvalidStream", Payload: []byte("not-a-proto"),
	}, w)

	assert.Empty(t, w.errors, "a streaming method must not answer with a unary error frame")
	frames := w.streamsSnapshot()
	require.Len(t, frames, 1, "the refusal must arrive as exactly one stream frame")
	assert.True(t, frames[0].GetIsError())
	assert.True(t, frames[0].GetEnd(), "and be terminal")
	assert.Equal(t, int32(codeInvalidArgument), frames[0].GetErrorCode())
	assert.Equal(t, "invalid request", frames[0].GetErrorMessage())
	assert.False(t, called, "the handler must never see a request that failed to decode")
}
