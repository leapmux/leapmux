package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// The following codes mirror the connect/gRPC codes used by sendInvalid /
// sendPermissionDenied / sendNotFoundError (see service.go).
const (
	codeInvalidArgument  = int32(3)
	codeNotFound         = int32(5)
	codePermissionDenied = int32(7)
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
		return &leapmuxv1.UpdateAgentSettingsRequest{AgentId: id, Settings: &leapmuxv1.AgentSettings{Model: "opus"}}
	}},
	{"SendControlResponse", func(id string) proto.Message {
		return &leapmuxv1.SendControlResponseRequest{AgentId: id, Content: []byte("{}")}
	}},
	{"ListAgentMessages", func(id string) proto.Message {
		return &leapmuxv1.ListAgentMessagesRequest{AgentId: id}
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
	{"SendInput", func(id string) proto.Message {
		return &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("x")}
	}},
	{"ResizeTerminal", func(id string) proto.Message {
		return &leapmuxv1.ResizeTerminalRequest{TerminalId: id, Cols: 80, Rows: 24}
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
			svc, d, w := setupTestService(t, "ws-1")
			svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
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
			svc, d, w := setupTestService(t, "ws-1")
			svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
			svc, d, w := setupTestService(t, "ws-1")
			svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
			svc, d, w := setupTestService(t, "ws-1")
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
			_, d, w := setupTestService(t, "ws-1")

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
			_, d, w := setupTestService(t, "ws-1")

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
		svc, d, w := setupTestService(t, "ws-1")
		svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
		seedAgent(t, svc, "agent-1", "ws-1")

		dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
			AgentId: "agent-1",
			Title:   "Renamed",
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})

	t.Run("ListAgentMessages", func(t *testing.T) {
		svc, d, w := setupTestService(t, "ws-1")
		svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
		seedAgent(t, svc, "agent-1", "ws-1")

		dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{AgentId: "agent-1"}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})
}

func TestAccessControl_TerminalHandlers_HappyPath(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
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
	svc, d, w := setupTestService(t, "ws-mine")
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
	svc, d, w := setupTestService(t, "ws-mine")
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
	svc, d, w := setupTestService(t, "ws-1")
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
	svc, d, w := setupTestService(t, "ws-src", "ws-dst")
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
	_, d, w := setupTestService(t, "ws-1")

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{
		WorkspaceId: "ws-other",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codePermissionDenied, w.errors[0].code)
	assert.Empty(t, w.responses)
}

func TestCleanupWorkspace_AllowsAccessibleWorkspace(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
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
