package service

import (
	"context"
	"path/filepath"
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
	// InterruptAgent is agent-ID-scoped via registerAgentGated.
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

// gatedMethodProbe describes one foreign-workspace denial probe for a method
// classified gateWorkspace or gateInBody. Completeness is enforced by
// TestAccessControl_GatedMethodProbesAreComplete against registerAllWithGates.
type gatedMethodProbe struct {
	name   string
	method string
	seed   func(t *testing.T, svc *Context)
	req    func() proto.Message
}

func seedForeignFileTab(t *testing.T, svc *Context, tabID, workspaceID string) {
	t.Helper()
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, nil)
	require.NoError(t, svc.FileTabPaths.Register(context.Background(), RegisterFileTabPathParams{
		OrgID:       "org-1",
		TabID:       tabID,
		WorkspaceID: workspaceID,
		FilePath:    "/tmp/probe.txt",
	}))
}

// gatedMethodProbes covers every gateWorkspace ∪ gateInBody method with at
// least one foreign-workspace denial. Derived entries reuse agentHandlerCases
// / terminalHandlerCases; the residue is hand-written.
var gatedMethodProbes = func() []gatedMethodProbe {
	var probes []gatedMethodProbe
	for _, tc := range agentHandlerCases {
		probes = append(probes, gatedMethodProbe{
			name:   tc.method,
			method: tc.method,
			seed:   func(t *testing.T, svc *Context) { seedAgent(t, svc, "agent-other", "ws-other") },
			req:    func() proto.Message { return tc.req("agent-other") },
		})
	}
	for _, tc := range terminalHandlerCases {
		probes = append(probes, gatedMethodProbe{
			name:   tc.method,
			method: tc.method,
			seed:   func(t *testing.T, svc *Context) { seedTerminal(t, svc, "term-other", "ws-other") },
			req:    func() proto.Message { return tc.req("term-other") },
		})
	}
	probes = append(probes,
		gatedMethodProbe{
			name:   "OpenAgent",
			method: "OpenAgent",
			seed:   func(*testing.T, *Context) {},
			req: func() proto.Message {
				return &leapmuxv1.OpenAgentRequest{WorkspaceId: "ws-other", WorkingDir: "/tmp"}
			},
		},
		gatedMethodProbe{
			name:   "OpenTerminal",
			method: "OpenTerminal",
			seed:   func(*testing.T, *Context) {},
			req: func() proto.Message {
				return &leapmuxv1.OpenTerminalRequest{WorkspaceId: "ws-other", WorkingDir: "/tmp"}
			},
		},
		gatedMethodProbe{
			name:   "WatchWorkspacePrivateEvents",
			method: "WatchWorkspacePrivateEvents",
			seed:   func(*testing.T, *Context) {},
			req: func() proto.Message {
				return &leapmuxv1.WatchWorkspacePrivateEventsRequest{WorkspaceId: "ws-other"}
			},
		},
		gatedMethodProbe{
			name:   "RegisterFileTabPath",
			method: "RegisterFileTabPath",
			seed:   func(*testing.T, *Context) {},
			req: func() proto.Message {
				return &leapmuxv1.RegisterFileTabPathRequest{
					TabId: "tab-1", OrgId: "org-1", WorkspaceId: "ws-other", FilePath: "/tmp/x",
				}
			},
		},
		gatedMethodProbe{
			name:   "CleanupWorkspace",
			method: "CleanupWorkspace",
			seed:   func(*testing.T, *Context) {},
			req: func() proto.Message {
				return &leapmuxv1.CleanupWorkspaceRequest{WorkspaceId: "ws-other"}
			},
		},
		gatedMethodProbe{
			name:   "GetFileTabPath",
			method: "GetFileTabPath",
			seed:   func(t *testing.T, svc *Context) { seedForeignFileTab(t, svc, "file-tab-other", "ws-other") },
			req: func() proto.Message {
				return &leapmuxv1.GetFileTabPathRequest{OrgId: "org-1", TabId: "file-tab-other"}
			},
		},
		gatedMethodProbe{
			name:   "RevokeFileTabPath",
			method: "RevokeFileTabPath",
			seed:   func(t *testing.T, svc *Context) { seedForeignFileTab(t, svc, "file-tab-other", "ws-other") },
			req: func() proto.Message {
				return &leapmuxv1.RevokeFileTabPathRequest{OrgId: "org-1", TabId: "file-tab-other"}
			},
		},
		gatedMethodProbe{
			name:   "RelocateFileTabPath/foreign-source",
			method: "RelocateFileTabPath",
			seed:   func(t *testing.T, svc *Context) { seedForeignFileTab(t, svc, "file-tab-other", "ws-other") },
			req: func() proto.Message {
				return &leapmuxv1.RelocateFileTabPathRequest{
					OrgId: "org-1", TabId: "file-tab-other", NewWorkspaceId: "ws-1",
				}
			},
		},
		gatedMethodProbe{
			name:   "RelocateFileTabPath/foreign-destination",
			method: "RelocateFileTabPath",
			seed: func(t *testing.T, svc *Context) {
				seedForeignFileTab(t, svc, "file-tab-mine", "ws-1")
			},
			req: func() proto.Message {
				return &leapmuxv1.RelocateFileTabPathRequest{
					OrgId: "org-1", TabId: "file-tab-mine", NewWorkspaceId: "ws-other",
				}
			},
		},
		gatedMethodProbe{
			name:   "MoveTabWorkspace",
			method: "MoveTabWorkspace",
			seed:   func(t *testing.T, svc *Context) { seedAgent(t, svc, "agent-other", "ws-other") },
			req: func() proto.Message {
				return &leapmuxv1.MoveTabWorkspaceRequest{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabId:   "agent-other", NewWorkspaceId: "ws-1",
				}
			},
		},
	)
	return probes
}()

func TestAccessControl_GatedMethods_DenyForeignWorkspace(t *testing.T) {
	for _, tc := range gatedMethodProbes {
		t.Run(tc.name, func(t *testing.T) {
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
			tc.seed(t, svc)

			dispatch(d, tc.method, tc.req(), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.name)
			assert.Equal(t, codePermissionDenied, w.errors[0].code, "%s: expected PERMISSION_DENIED", tc.name)
			// Pin the denial message too, not just the code: a change that keeps
			// PERMISSION_DENIED but blanks or leaks the reason (e.g. echoing the
			// workspace_id) would otherwise slip through. Recovers the message
			// assertion the deleted open_workspace_required_test.go carried, now
			// across every gated method rather than just OpenAgent/OpenTerminal.
			assert.Contains(t, w.errors[0].message, "not accessible", "%s: denial should name the access failure", tc.name)
			assert.Empty(t, w.responses, "%s: no response should be sent", tc.name)
		})
	}
}

func TestAccessControl_GatedMethodProbesAreComplete(t *testing.T) {
	svc, _, _ := setupTestService(t)
	gates := registerAllWithGates(channel.NewDispatcher(), svc)

	var expected []string
	for method, gate := range gates {
		if gate == gateWorkspace || gate == gateInBody {
			expected = append(expected, method)
		}
	}

	seen := make(map[string]bool)
	var probed []string
	for _, p := range gatedMethodProbes {
		if !seen[p.method] {
			seen[p.method] = true
			probed = append(probed, p.method)
		}
	}
	assert.ElementsMatch(t, expected, probed,
		"gatedMethodProbes must cover exactly the gateWorkspace ∪ gateInBody methods from registerAllWithGates")
}

// TestAccessControl_WorkspaceFieldMethods_EmptyWorkspaceID covers the
// gateWorkspace methods whose workspace id is a request field (as opposed to
// a loaded row): registerWorkspaceGated's empty-id branch must fire before
// any handler code runs. Row-gated methods' empty-id branch is covered by
// the *_EmptyID tests driven from agentHandlerCases / terminalHandlerCases.
func TestAccessControl_WorkspaceFieldMethods_EmptyWorkspaceID(t *testing.T) {
	cases := []struct {
		method string
		req    proto.Message
	}{
		{"OpenAgent", &leapmuxv1.OpenAgentRequest{WorkingDir: "/tmp"}},
		{"OpenTerminal", &leapmuxv1.OpenTerminalRequest{WorkingDir: "/tmp"}},
		{"WatchWorkspacePrivateEvents", &leapmuxv1.WatchWorkspacePrivateEventsRequest{}},
		// Every other required field is set, so the assertion also pins the
		// ordering: workspace_id is validated in the registrar BEFORE the
		// handler's own required-field check gets a say.
		{"RegisterFileTabPath", &leapmuxv1.RegisterFileTabPathRequest{
			TabId: "tab-1", OrgId: "org-1", FilePath: "/tmp/x",
		}},
		{"CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t, withWorkspaces("ws-1"))

			dispatch(d, tc.method, tc.req, w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeInvalidArgument, w.errors[0].code, "%s: expected INVALID_ARGUMENT", tc.method)
			assert.Equal(t, "workspace_id is required", w.errors[0].message, tc.method)
			assert.Empty(t, w.responses, "%s: no response should be sent", tc.method)
		})
	}
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
// Methods are enumerated from the gateOwnerOnly bucket of registerAllWithGates
// rather than by replaying the four family register functions. An empty payload
// suffices: ownerOnlyRegistrar.gate runs requireWorkerOwner BEFORE the handler
// unmarshals anything, so a non-owner is refused without a valid request ever
// being built -- which is itself the property worth pinning (an ungated handler
// would get as far as parsing attacker-supplied bytes).
//
// Workspace-scoped gating is enforced structurally via registerWorkspaceGated /
// registerAgentGated / registerTerminalGated (and Tracked / ForRestart variants),
// with gateInBody residue covered by gatedMethodProbes. Completeness is asserted
// by TestAccessControl_GatedMethodProbesAreComplete and
// TestEveryRegisteredMethodIsClassified.
func TestMachineScopedFamiliesAreOwnerOnly(t *testing.T) {
	svc, d, _ := setupTestService(t)
	gates := registerAllWithGates(channel.NewDispatcher(), svc)

	var methods []string
	for method, gate := range gates {
		if gate == gateOwnerOnly {
			methods = append(methods, method)
		}
	}
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

// TestListAvailableShells_OwnerAllowed pins the ALLOW side of the
// registerOwnerOnly gate the capability probes moved behind: the worker owner
// (the identity the local-IPC remote CLI dispatches with) must still be able
// to enumerate installed shells. The deny side is covered per-method by
// TestMachineScopedFamiliesAreOwnerOnly; ListAvailableProviders shares the
// identical registerOwnerOnly wrapper, so one end-to-end allow probe covers
// the gate (its body forks discovery subprocesses, too slow for a unit test).
func TestListAvailableShells_OwnerAllowed(t *testing.T) {
	_, d, w := setupTestService(t)

	dispatch(d, "ListAvailableShells", &leapmuxv1.ListAvailableShellsRequest{}, w)

	require.Empty(t, w.errors, "the owner must pass the owner-only gate")
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAvailableShellsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetShells(), "owner should see at least one installed shell")
}

// TestEveryRegisteredMethodIsClassified is the default-deny companion: EVERY
// method registerAllWithGates wires must appear in the gate map, and the two
// open-by-design buckets are pinned with explicit lists so additions are
// reviewed decisions. Disjointness (no method recorded twice) is enforced by
// registrar.record's duplicate panic at registration time.
func TestEveryRegisteredMethodIsClassified(t *testing.T) {
	svc, d, _ := setupTestService(t)
	gates := registerAllWithGates(channel.NewDispatcher(), svc)

	var gated []string
	for method := range gates {
		gated = append(gated, method)
	}
	assert.ElementsMatch(t, d.Methods(), gated,
		"every method RegisterAll wires must have a recorded methodGate")

	var setFilter, ungated []string
	for method, gate := range gates {
		switch gate {
		case gateSetFilter:
			setFilter = append(setFilter, method)
		case gateNone:
			ungated = append(ungated, method)
		}
	}
	assert.ElementsMatch(t, []string{"ListAgents", "ListTerminals", "WatchEvents"}, setFilter,
		"gateSetFilter additions must be an explicit reviewed decision")
	assert.ElementsMatch(t, []string{"Ping"}, ungated,
		"gateNone additions must be an explicit reviewed decision")
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
	svc, d, w := setupTestService(t)

	dispatch(d, "GetWorkerSystemInfo", &leapmuxv1.GetWorkerSystemInfoRequest{}, w)
	require.Empty(t, w.errors, "the owner must not be refused")
	require.Len(t, w.responses, 1)

	// An absolute path outside HomeDir still resolves for the owner. Use the
	// parent of HomeDir rather than a hard-coded path like /etc: it is always
	// absolute, always exists, and is outside the home on every GOOS, so the
	// test does not regress on Windows where /etc fails SanitizePath.
	outsideHome := filepath.Dir(svc.HomeDir)
	w2 := newTestWriter()
	dispatch(d, "StatFile", &leapmuxv1.StatFileRequest{Path: outsideHome}, w2)
	require.Empty(t, w2.errors, "the owner may stat outside their home directory")
}
