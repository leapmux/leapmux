package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestRegisterWorkspaceGated_InvalidPayload(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerWorkspaceGated(r, "ProbeOpen",
		func(context.Context, string, *leapmuxv1.OpenAgentRequest, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method:  "ProbeOpen",
		Payload: []byte("not-a-proto"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Equal(t, "invalid request", w.errors[0].message)
	assert.False(t, called)
}

func TestRegisterWorkspaceGated_EmptyWorkspaceID(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerWorkspaceGated(r, "ProbeOpen",
		func(context.Context, string, *leapmuxv1.OpenAgentRequest, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.OpenAgentRequest{})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeOpen", Payload: payload,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Equal(t, "workspace_id is required", w.errors[0].message)
	assert.False(t, called)
}

func TestRegisterWorkspaceGated_ForeignWorkspaceShortCircuits(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerWorkspaceGated(r, "ProbeOpen",
		func(context.Context, string, *leapmuxv1.OpenAgentRequest, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.OpenAgentRequest{WorkspaceId: "ws-other"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeOpen", Payload: payload,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.False(t, called)
}

func TestRegisterWorkspaceGated_PassesDecodedRequest(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotWS string
	registerWorkspaceGated(r, "ProbeOpen",
		func(_ context.Context, _ string, req *leapmuxv1.OpenAgentRequest, sender channel.ResponseWriter) {
			gotWS = req.GetWorkspaceId()
			sendProtoResponse(sender, &leapmuxv1.OpenAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.OpenAgentRequest{WorkspaceId: "ws-1"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeOpen", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "ws-1", gotWS)
}

func TestRegisterAgentGated_PassesLoadedRow(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-1", "ws-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID, gotWS string
	registerAgentGated(r, "ProbeAgent",
		func(_ context.Context, _ string, _ *leapmuxv1.RenameAgentRequest, row db.Agent, sender channel.ResponseWriter) {
			gotID = row.ID
			gotWS = row.WorkspaceID
			sendProtoResponse(sender, &leapmuxv1.RenameAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RenameAgentRequest{AgentId: "agent-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgent", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
	assert.Equal(t, "ws-1", gotWS)
}

func TestRegisterAgentGated_ForeignWorkspaceShortCircuits(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-other", "ws-other")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerAgentGated(r, "ProbeAgent",
		func(context.Context, string, *leapmuxv1.RenameAgentRequest, db.Agent, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RenameAgentRequest{AgentId: "agent-other", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgent", Payload: payload,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.False(t, called)
}

func TestRegisterAgentGatedByID_PassesDecodedRequest(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-1", "ws-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID string
	registerAgentGatedByID(r, "ProbeAgentID",
		func(_ context.Context, _ string, req *leapmuxv1.InterruptAgentRequest, sender channel.ResponseWriter) {
			gotID = req.GetAgentId()
			sendProtoResponse(sender, &leapmuxv1.InterruptAgentResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.InterruptAgentRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgentID", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "agent-1", gotID)
}

func TestRegisterAgentGatedByID_ForeignWorkspaceShortCircuits(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-other", "ws-other")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerAgentGatedByID(r, "ProbeAgentID",
		func(context.Context, string, *leapmuxv1.InterruptAgentRequest, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.InterruptAgentRequest{AgentId: "agent-other"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeAgentID", Payload: payload,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.False(t, called)
}

func TestRegisterTerminalGatedByID_ForeignWorkspaceShortCircuits(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedTerminal(t, svc, "term-other", "ws-other")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	called := false
	registerTerminalGatedByID(r, "ProbeTermID",
		func(context.Context, string, *leapmuxv1.SendInputRequest, channel.ResponseWriter) {
			called = true
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.SendInputRequest{TerminalId: "term-other"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeTermID", Payload: payload,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.False(t, called)
}

func TestRegisterTerminalGated_PassesLoadedRow(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedTerminal(t, svc, "term-1", "ws-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotID, gotWS string
	registerTerminalGated(r, "ProbeTerm",
		func(_ context.Context, _ string, _ *leapmuxv1.UpdateTerminalTitleRequest, row db.Terminal, sender channel.ResponseWriter) {
			gotID = row.ID
			gotWS = row.WorkspaceID
			sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.UpdateTerminalTitleRequest{TerminalId: "term-1", Title: "x"})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeTerm", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "term-1", gotID)
	assert.Equal(t, "ws-1", gotWS)
}

func TestRegisterTerminalForRestartGated_PassesRow(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	seedTerminal(t, svc, "term-1", "ws-1")
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	var gotWS string
	registerTerminalForRestartGated(r, "ProbeRestart",
		func(_ context.Context, _ string, _ *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender channel.ResponseWriter) {
			gotWS = row.WorkspaceID
			sendProtoResponse(sender, &leapmuxv1.RestartTerminalResponse{})
		})

	w := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.RestartTerminalRequest{TerminalId: "term-1", Cols: 80, Rows: 25})
	require.NoError(t, err)
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "ProbeRestart", Payload: payload,
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "ws-1", gotWS)
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
			name: "registerAgentGatedByIDTracked",
			seed: func(t *testing.T, svc *Service) { seedAgent(t, svc, "agent-1", "ws-1") },
			register: func(r registrar, method string, block func()) {
				registerAgentGatedByIDTracked(r, method,
					func(context.Context, string, *leapmuxv1.CloseAgentRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"},
		},
		{
			name: "registerTerminalGatedByIDTracked",
			seed: func(t *testing.T, svc *Service) { seedTerminal(t, svc, "term-1", "ws-1") },
			register: func(r registrar, method string, block func()) {
				registerTerminalGatedByIDTracked(r, method,
					func(context.Context, string, *leapmuxv1.CloseTerminalRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.CloseTerminalRequest{TerminalId: "term-1"},
		},
		{
			name: "registerInBodyGatedTracked",
			seed: func(*testing.T, *Service) {},
			register: func(r registrar, method string, block func()) {
				registerInBodyGatedTracked(r, method,
					func(context.Context, string, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
						block()
					})
			},
			req: &leapmuxv1.RevokeFileTabPathRequest{OrgId: "org-1", TabId: "tab-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
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
			d.DispatchAsync(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
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
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	d := channel.NewDispatcher()
	r := newRegistrar(d, svc)

	registerUngated(r, "Dup", func(context.Context, string, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {})
	assert.Panics(t, func() {
		registerUngated(r, "Dup", func(context.Context, string, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {})
	})
}
