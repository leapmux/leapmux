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

// The following codes mirror the connect/gRPC codes used by sendInvalid /
// sendPermissionDenied / sendNotFoundError (see service.go).
const (
	codeInvalidArgument    = int32(3)
	codeNotFound           = int32(5)
	codePermissionDenied   = int32(7)
	codeFailedPrecondition = int32(9)
)

// seedAgent and seedTerminal create minimal DB rows in the given workspace.
func seedAgent(t *testing.T, svc *Context, agentID, workspaceID string) {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))
}

func seedTerminal(t *testing.T, svc *Context, terminalID, workspaceID string) {
	t.Helper()
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:          terminalID,
		WorkspaceID: workspaceID,
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Screen:      []byte{},
	}))
}

// agentHandlerCases enumerates the agent-ID-scoped handlers we gate via
// requireAccessibleAgent. Each entry builds the request proto for a given
// agent ID and returns the RPC method name to dispatch.
type agentHandlerCase struct {
	method string
	req    func(agentID string) proto.Message
}

var agentHandlerCases = []agentHandlerCase{
	{"CloseAgent", func(id string) proto.Message {
		return &leapmuxv1.CloseAgentRequest{AgentId: id}
	}},
	{"SendAgentMessage", func(id string) proto.Message {
		return &leapmuxv1.SendAgentMessageRequest{AgentId: id, Content: "hello"}
	}},
	{"SendAgentRawMessage", func(id string) proto.Message {
		return &leapmuxv1.SendAgentRawMessageRequest{AgentId: id, Content: "{}"}
	}},
	{"RenameAgent", func(id string) proto.Message {
		return &leapmuxv1.RenameAgentRequest{AgentId: id, Title: "renamed"}
	}},
	{"DeleteAgentMessage", func(id string) proto.Message {
		return &leapmuxv1.DeleteAgentMessageRequest{AgentId: id, MessageId: "msg-1"}
	}},
	{"UpdateAgentSettings", func(id string) proto.Message {
		return &leapmuxv1.UpdateAgentSettingsRequest{AgentId: id, Settings: &leapmuxv1.AgentSettings{Options: map[string]string{"model": "opus"}}}
	}},
	{"SendControlResponse", func(id string) proto.Message {
		return &leapmuxv1.SendControlResponseRequest{AgentId: id, Content: []byte("{}")}
	}},
	{"ListAgentMessages", func(id string) proto.Message {
		return &leapmuxv1.ListAgentMessagesRequest{AgentId: id}
	}},
	{"GetAgentMessage", func(id string) proto.Message {
		return &leapmuxv1.GetAgentMessageRequest{AgentId: id, Seq: 1}
	}},
	{"ListMessageMarks", func(id string) proto.Message {
		return &leapmuxv1.ListMessageMarksRequest{AgentId: id}
	}},
	// InterruptAgent is agent-ID-scoped via requireAccessibleAgent (agent.go),
	// but was the one such handler missing from this table -- so its
	// inaccessible-workspace / not-found / empty-id denials had no coverage.
	// The gate is correct today; these cases guard against a future edit that
	// drops it (which would let InterruptAgent reach svc.Agents.Interrupt for a
	// workspace the caller cannot access).
	{"InterruptAgent", func(id string) proto.Message {
		return &leapmuxv1.InterruptAgentRequest{AgentId: id}
	}},
}

// terminalHandlerCases enumerates terminal-ID-scoped handlers gated via
// requireAccessibleTerminal.
type terminalHandlerCase struct {
	method string
	req    func(terminalID string) proto.Message
}

var terminalHandlerCases = []terminalHandlerCase{
	{"CloseTerminal", func(id string) proto.Message {
		return &leapmuxv1.CloseTerminalRequest{TerminalId: id}
	}},
	{"RestartTerminal", func(id string) proto.Message {
		return &leapmuxv1.RestartTerminalRequest{TerminalId: id, Cols: 80, Rows: 25}
	}},
	{"SendInput", func(id string) proto.Message {
		return &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("x")}
	}},
	{"ResizeTerminal", func(id string) proto.Message {
		return &leapmuxv1.ResizeTerminalRequest{TerminalId: id, Cols: 80, Rows: 25}
	}},
	{"UpdateTerminalTitle", func(id string) proto.Message {
		return &leapmuxv1.UpdateTerminalTitleRequest{TerminalId: id, Title: "renamed"}
	}},
}

// TestAccessControl_AgentHandlers_RejectInaccessibleWorkspace verifies that
// every agent-ID-scoped handler rejects agents whose workspace is not in the
// channel's accessible set.
func TestAccessControl_AgentHandlers_RejectInaccessibleWorkspace(t *testing.T) {
	for _, tc := range agentHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
			seedAgent(t, svc, "agent-other", "ws-other")

			dispatch(d, tc.method, tc.req("agent-other"), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codePermissionDenied, w.errors[0].code, "%s: expected PERMISSION_DENIED", tc.method)
			assert.Empty(t, w.responses, "%s: no response should be sent", tc.method)
		})
	}
}

// TestAccessControl_AgentHandlers_NotFound verifies that agent-ID-scoped
// handlers return NOT_FOUND when the agent does not exist.
func TestAccessControl_AgentHandlers_NotFound(t *testing.T) {
	for _, tc := range agentHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t, withWorkspaces("ws-1"))

			dispatch(d, tc.method, tc.req("agent-missing"), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeNotFound, w.errors[0].code, "%s: expected NOT_FOUND", tc.method)
			assert.Empty(t, w.responses)
		})
	}
}

// TestAccessControl_AgentHandlers_EmptyID verifies INVALID_ARGUMENT when the
// agent_id is not provided.
func TestAccessControl_AgentHandlers_EmptyID(t *testing.T) {
	for _, tc := range agentHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t, withWorkspaces("ws-1"))

			dispatch(d, tc.method, tc.req(""), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeInvalidArgument, w.errors[0].code, "%s: expected INVALID_ARGUMENT", tc.method)
		})
	}
}

// TestAccessControl_TerminalHandlers_RejectInaccessibleWorkspace is the
// terminal counterpart.
func TestAccessControl_TerminalHandlers_RejectInaccessibleWorkspace(t *testing.T) {
	for _, tc := range terminalHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
			seedTerminal(t, svc, "term-other", "ws-other")

			dispatch(d, tc.method, tc.req("term-other"), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codePermissionDenied, w.errors[0].code, "%s: expected PERMISSION_DENIED", tc.method)
			assert.Empty(t, w.responses)
		})
	}
}

func TestAccessControl_TerminalHandlers_NotFound(t *testing.T) {
	for _, tc := range terminalHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t, withWorkspaces("ws-1"))

			dispatch(d, tc.method, tc.req("term-missing"), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeNotFound, w.errors[0].code, "%s: expected NOT_FOUND", tc.method)
			assert.Empty(t, w.responses)
		})
	}
}

func TestAccessControl_TerminalHandlers_EmptyID(t *testing.T) {
	for _, tc := range terminalHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t, withWorkspaces("ws-1"))

			dispatch(d, tc.method, tc.req(""), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeInvalidArgument, w.errors[0].code, "%s: expected INVALID_ARGUMENT", tc.method)
		})
	}
}

// Happy-path smoke tests — dispatching against an accessible resource should
// not produce an access-control error. We pick representative handlers that
// cover both the simple "look up row" path (RenameAgent, UpdateTerminalTitle)
// and the "use returned row" path that exercises the second return value
// (ListAgentMessages).

func TestAccessControl_AgentHandlers_HappyPath(t *testing.T) {
	t.Run("RenameAgent", func(t *testing.T) {
		svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
		seedAgent(t, svc, "agent-1", "ws-1")

		dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
			AgentId: "agent-1",
			Title:   "Renamed",
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})

	t.Run("ListAgentMessages", func(t *testing.T) {
		svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
		seedAgent(t, svc, "agent-1", "ws-1")

		dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{AgentId: "agent-1"}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})
}

func TestAccessControl_TerminalHandlers_HappyPath(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	seedTerminal(t, svc, "term-1", "ws-1")

	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		TerminalId: "term-1",
		Title:      "New Title",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}

// MoveTabWorkspace-specific tests. The source and destination checks both
// need coverage because the pre-audit bug was that only the destination was
// validated — any tab could be stolen into a workspace the caller owns.

func TestMoveTabWorkspace_RejectsStealingAgentFromOtherWorkspace(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-mine"))
	seedAgent(t, svc, "agent-theirs", "ws-theirs")

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "agent-theirs",
		NewWorkspaceId: "ws-mine",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.Empty(t, w.responses)

	// The agent must still belong to the original workspace.
	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-theirs")
	require.NoError(t, err)
	assert.Equal(t, "ws-theirs", row.WorkspaceID, "agent must not have been moved")
}

func TestMoveTabWorkspace_RejectsStealingTerminalFromOtherWorkspace(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-mine"))
	seedTerminal(t, svc, "term-theirs", "ws-theirs")

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabId:          "term-theirs",
		NewWorkspaceId: "ws-mine",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)

	row, err := svc.Queries.GetTerminal(context.Background(), "term-theirs")
	require.NoError(t, err)
	assert.Equal(t, "ws-theirs", row.WorkspaceID, "terminal must not have been moved")
}

func TestMoveTabWorkspace_RejectsInaccessibleDestination(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-1", "ws-1")

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "agent-1",
		NewWorkspaceId: "ws-other",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-1", row.WorkspaceID, "agent must not have been moved")
}

func TestMoveTabWorkspace_AllowsMoveBetweenAccessibleWorkspaces(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-src", "ws-dst"))
	seedAgent(t, svc, "agent-1", "ws-src")

	dispatch(d, "MoveTabWorkspace", &leapmuxv1.MoveTabWorkspaceRequest{
		TabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:          "agent-1",
		NewWorkspaceId: "ws-dst",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-dst", row.WorkspaceID)
}

// CleanupWorkspace tests. The accessible set is add-only per channel, so a
// previously-accessible workspace (freshly deleted at the hub) stays cleanable.
// A workspace never added to the set must be rejected.

func TestCleanupWorkspace_RejectsInaccessibleWorkspace(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{
		WorkspaceId: "ws-other",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.Empty(t, w.responses)
}

func TestCleanupWorkspace_AllowsAccessibleWorkspace(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	seedAgent(t, svc, "agent-1", "ws-1")

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{
		WorkspaceId: "ws-1",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "agent should be soft-closed by cleanup")
}

// The machine-scoped families -- file, git, sysinfo, tunnel -- must admit ONLY
// the worker's registered owner.
//
// Their reach is the whole machine, not a workspace: validate.SanitizePath
// normalizes a path and blocks traversal, but does NOT confine it to a root, so
// any absolute path is fair game. That is fine for the owner (their agents already
// run as them on their own machine) and must be denied to everyone else -- above
// all a delegation bearer, which is pinned to one workspace and handed to a
// prompt-injectable agent.
//
// Every family here is gated structurally: registerFileHandlers, registerGitHandlers,
// registerSysInfoHandlers and registerTunnelHandlers all take an ownerOnlyRegistrar
// rather than the raw *channel.Dispatcher, so an ungated handler in them cannot be
// written.
//
// The methods are ENUMERATED from the dispatcher rather than listed here. A
// hand-maintained table backstops only the methods someone remembered to add to it
// -- it silently stops covering each handler registered after it was written, which
// is the same "a line each author must remember" flaw the registrar exists to
// remove, reintroduced in the test that is supposed to guard the registrar. Reading
// the set back from the dispatcher means a new machine-scoped handler is covered the
// moment it is registered, and a handler MOVED off the registrar onto the raw
// dispatcher fails here instead of passing unnoticed.
//
// An empty payload suffices: ownerOnlyRegistrar.gate runs requireWorkerOwner BEFORE
// the handler unmarshals anything, so a non-owner is refused without a valid request
// ever being built -- which is itself the property worth pinning (an ungated handler
// would get as far as parsing attacker-supplied bytes).
//
// Workspace-scoped gating (as opposed to the machine-scoped, owner-only gating
// enumerated here) has no equivalent mechanical enforcement -- it gates in-body
// per handler and its coverage rests on the hand-maintained agentHandlerCases /
// terminalHandlerCases tables above, exactly the "a line each author must
// remember" flaw this comment describes. InterruptAgent is workspace-gated in
// code but appears in zero test files here -- proof the drift is already
// real. See https://github.com/leapmux/leapmux/issues/284 for a typed-
// registrar shape that closes the gap for most methods.
func TestMachineScopedFamiliesAreOwnerOnly(t *testing.T) {
	svc, d, _ := setupTestService(t)

	// A throwaway dispatcher carrying ONLY the machine-scoped families names the set
	// of methods that must be owner-only. Enumerating from the production dispatcher
	// (d) instead would sweep in the workspace-scoped families, which legitimately
	// serve non-owners.
	//
	// The dispatch below then runs against the PRODUCTION dispatcher, so the two
	// halves cross-check each other: this names what must be gated, and RegisterAll's
	// wiring is what actually answers.
	ownerOnlyDispatcher := channel.NewDispatcher()
	ownerOnly := ownerOnlyRegistrar{d: ownerOnlyDispatcher, svc: svc}
	registerFileHandlers(ownerOnly, svc)
	registerGitHandlers(ownerOnly, svc)
	registerSysInfoHandlers(ownerOnly, svc)
	registerTunnelHandlers(ownerOnly)

	methods := ownerOnlyDispatcher.Methods()
	require.NotEmpty(t, methods, "the machine-scoped families must register something")

	for _, method := range methods {
		t.Run(method+" denies a non-owner", func(t *testing.T) {
			w := newTestWriter()
			// "user-2" holds a valid channel but does not own this worker.
			d.DispatchWith(context.Background(), "user-2", &leapmuxv1.InnerRpcRequest{
				Method: method,
			}, w)

			require.Len(t, w.errors, 1, "a non-owner must be refused")
			assert.Equal(t, codePermissionDenied, w.errors[0].code)
			assert.Empty(t, w.responses, "a refused call must return no data")
		})
	}
}

// The default-deny companion to the test above: EVERY method RegisterAll wires
// must be CLASSIFIED -- registered by a machine-scoped family (owner-only by
// construction) or by one of the workspace-/channel-scoped families listed here.
//
// The owner-only test enumerates its methods from the dispatcher, but the FAMILY
// list feeding that dispatcher is still written by hand, so a brand-new
// machine-scoped family wired to the raw dispatcher in RegisterAll would ship
// covered by neither list and no test would notice. This closes that gap: an
// unclassified method fails here until its author decides which side it belongs
// on, and classifying it machine-scoped puts it under the denial test above
// automatically.
func TestEveryRegisteredMethodIsClassified(t *testing.T) {
	svc, d, _ := setupTestService(t)

	// The machine-scoped families, exactly as RegisterAll hands them the gated
	// registrar.
	ownerOnlyDispatcher := channel.NewDispatcher()
	ownerOnly := ownerOnlyRegistrar{d: ownerOnlyDispatcher, svc: svc}
	registerFileHandlers(ownerOnly, svc)
	registerGitHandlers(ownerOnly, svc)
	registerSysInfoHandlers(ownerOnly, svc)
	registerTunnelHandlers(ownerOnly)

	// Everything RegisterAll deliberately leaves on the raw dispatcher: ping plus
	// the workspace-scoped families, which legitimately serve non-owners and gate
	// on the Hub-supplied accessible-workspace set instead.
	workspaceScopedDispatcher := channel.NewDispatcher()
	registerPingHandler(workspaceScopedDispatcher, svc)
	registerTerminalHandlers(workspaceScopedDispatcher, svc)
	registerAgentHandlers(workspaceScopedDispatcher, svc)
	registerCleanupHandlers(workspaceScopedDispatcher, svc)
	registerTabMoveHandlers(workspaceScopedDispatcher, svc)

	// No method may sit on both sides; the union must be exactly what RegisterAll
	// produced -- an extra production method is unclassified, a missing one means a
	// family listed here fell out of RegisterAll.
	classified := make(map[string]bool)
	for _, m := range ownerOnlyDispatcher.Methods() {
		classified[m] = true
	}
	for _, m := range workspaceScopedDispatcher.Methods() {
		require.False(t, classified[m],
			"method %q is registered by both a machine-scoped and a workspace-scoped family", m)
		classified[m] = true
	}

	var expected []string
	expected = append(expected, ownerOnlyDispatcher.Methods()...)
	expected = append(expected, workspaceScopedDispatcher.Methods()...)
	assert.ElementsMatch(t, expected, d.Methods(),
		"every method RegisterAll wires must be claimed by exactly one family list in this test -- classify new families as machine-scoped (owner-only) or workspace-scoped")
}

// The owner is written by the connect loop and read by handlers on their own
// goroutines, so the two genuinely race and the field must be atomic.
//
// Within ONE connection the DispatchAsync goroutine spawn orders them. A RECONNECT
// does not: Manager.CloseAll cancels session contexts WITHOUT waiting for in-flight
// handlers, so a handler from the previous connection can still be inside
// requireWorkerOwner while the next connection's receive loop delivers WorkerIdentity
// and writes. The value is identical every time, which is precisely what makes a
// plain field's race invisible -- nothing misbehaves until the detector or a torn
// read finds it.
//
// The suite does not run under -race by default (task test-backend does not pass
// it), so this only bites under `go test -race ./internal/worker/service/`. It
// still earns its place: it is the only thing that exercises the write against
// concurrent gate reads at all, so a future revert to a plain field is caught the
// moment anyone runs the detector.
func TestRegisteredByConcurrentSetAndGate(t *testing.T) {
	svc := &Context{}
	svc.SetRegisteredBy("user-1")

	const rounds = 200
	var wg sync.WaitGroup

	// The connect loop: re-delivers the owner on every reconnect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			svc.SetRegisteredBy("user-1")
		}
	}()

	// Handlers left over from a previous connection, still gating.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				w := newTestWriter()
				requireWorkerOwner(svc, "user-1", channel.NewSender(w))
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, "user-1", svc.RegisteredBy(), "the owner must survive concurrent access")
}

// An empty caller id must NOT match an empty RegisteredBy.
//
// The gate is a string compare, so two empty ids satisfy it -- and a worker whose
// RegisteredBy never got populated (the standalone path reads it from a state file
// with `omitempty` and, unlike solo mode, backfills nothing) would then hand the
// whole machine to a caller the Hub named with an empty user id. A gate that exists
// to fail closed must not fail open on the one input it cannot judge.
func TestRequireWorkerOwnerRefusesEmptyIdentities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		userID       string
		registeredBy string
	}{
		{"both empty", "", ""},
		{"empty caller against a real owner", "", "user-1"},
		{"real caller against an unset owner", "user-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Context{}
			svc.SetRegisteredBy(tc.registeredBy)
			w := newTestWriter()
			assert.False(t, requireWorkerOwner(svc, tc.userID, channel.NewSender(w)),
				"an empty identity must never satisfy the owner gate")
			require.Len(t, w.errors, 1, "the refusal is reported to the caller")
			assert.Equal(t, codePermissionDenied, w.errors[0].code)
		})
	}
}

// ...and the owner keeps unrestricted reach, including outside the home directory.
// This is deliberate: the worker and its agents already have it.
func TestMachineScopedFamiliesAllowOwnerOutsideHome(t *testing.T) {
	_, d, w := setupTestService(t)

	dispatch(d, "GetWorkerSystemInfo", &leapmuxv1.GetWorkerSystemInfoRequest{}, w)
	require.Empty(t, w.errors, "the owner must not be refused")
	require.Len(t, w.responses, 1)

	// An absolute path outside HomeDir still resolves for the owner.
	w2 := newTestWriter()
	dispatch(d, "StatFile", &leapmuxv1.StatFileRequest{Path: "/etc"}, w2)
	require.Empty(t, w2.errors, "the owner may stat outside their home directory")
}
