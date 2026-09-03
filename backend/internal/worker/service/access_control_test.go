package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
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

// seedAgent and seedTerminal create minimal DB rows.
func seedAgent(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:         agentID,
		WorkingDir: t.TempDir(),
		HomeDir:    t.TempDir(),
	}))
}

func seedTerminal(t *testing.T, svc *Service, terminalID string) {
	t.Helper()
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         terminalID,
		WorkingDir: t.TempDir(),
		HomeDir:    t.TempDir(),
		Screen:     []byte{},
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

// useridFromTest mints a UserID for tests; empty input yields the zero value
// (matching userid.New's fail-closed mint).
func useridFromTest(s string) userid.UserID {
	u, _ := userid.New(s)
	return u
}

// TestAccessControl_AgentHandlers_NotFound verifies that agent-ID-scoped
// handlers return NOT_FOUND when the agent does not exist.
func TestAccessControl_AgentHandlers_NotFound(t *testing.T) {
	t.Parallel()

	for _, tc := range agentHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t)

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
	t.Parallel()

	for _, tc := range agentHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t)

			dispatch(d, tc.method, tc.req(""), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeInvalidArgument, w.errors[0].code, "%s: expected INVALID_ARGUMENT", tc.method)
		})
	}
}

func TestAccessControl_TerminalHandlers_NotFound(t *testing.T) {
	t.Parallel()

	for _, tc := range terminalHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t)

			dispatch(d, tc.method, tc.req("term-missing"), w)

			require.Len(t, w.errors, 1, "%s: expected one error", tc.method)
			assert.Equal(t, codeNotFound, w.errors[0].code, "%s: expected NOT_FOUND", tc.method)
			assert.Empty(t, w.responses)
		})
	}
}

func TestAccessControl_TerminalHandlers_EmptyID(t *testing.T) {
	t.Parallel()

	for _, tc := range terminalHandlerCases {
		t.Run(tc.method, func(t *testing.T) {
			_, d, w := setupTestService(t)

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
	t.Parallel()

	t.Run("RenameAgent", func(t *testing.T) {
		svc, d, w := setupTestService(t)
		seedAgent(t, svc, "agent-1")

		dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
			AgentId: "agent-1",
			Title:   "Renamed",
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})

	t.Run("ListAgentMessages", func(t *testing.T) {
		svc, d, w := setupTestService(t)
		seedAgent(t, svc, "agent-1")

		dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{AgentId: "agent-1"}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})
}

func TestAccessControl_TerminalHandlers_HappyPath(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedTerminal(t, svc, "term-1")

	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		TerminalId: "term-1",
		Title:      "New Title",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}

// ownerGatedProbe describes one non-owner denial probe for a method the
// registrar wires behind the owner gate. Completeness is enforced by
// TestAccessControl_OwnerGatedProbesAreComplete against registerAllWithGates.
//
// The gate replaced here was the per-workspace one. It is gone because there is
// nothing left for it to narrow: a Worker is registered by exactly one user and
// stores no workspace id, so "the caller owns this Worker" and "the caller owns
// every row it holds" are the same statement. What still has to be proved, and
// is proved below, is that EVERY method is behind that one gate -- an ungated
// handler would parse and act on a stranger's request.
type ownerGatedProbe struct {
	name   string
	method string
	req    func() proto.Message
}

// ownerGatedProbes covers every gateOwnerOnly method that takes a typed request
// with at least one non-owner denial. Derived entries reuse agentHandlerCases /
// terminalHandlerCases; the residue is hand-written.
var ownerGatedProbes = func() []ownerGatedProbe {
	var probes []ownerGatedProbe
	for _, tc := range agentHandlerCases {
		probes = append(probes, ownerGatedProbe{
			name:   tc.method,
			method: tc.method,
			req:    func() proto.Message { return tc.req("agent-1") },
		})
	}
	for _, tc := range terminalHandlerCases {
		probes = append(probes, ownerGatedProbe{
			name:   tc.method,
			method: tc.method,
			req:    func() proto.Message { return tc.req("term-1") },
		})
	}
	probes = append(probes,
		ownerGatedProbe{"OpenAgent", "OpenAgent", func() proto.Message {
			return &leapmuxv1.OpenAgentRequest{WorkingDir: "/tmp"}
		}},
		ownerGatedProbe{"OpenTerminal", "OpenTerminal", func() proto.Message {
			return &leapmuxv1.OpenTerminalRequest{WorkingDir: "/tmp"}
		}},
		ownerGatedProbe{"WatchWorkerPrivateEvents", "WatchWorkerPrivateEvents", func() proto.Message {
			return &leapmuxv1.WatchWorkerPrivateEventsRequest{}
		}},
		ownerGatedProbe{"RegisterTabPayload", "RegisterTabPayload", func() proto.Message {
			return &leapmuxv1.RegisterTabPayloadRequest{TabId: "tab-1", Payload: fileTabPayload("/tmp/x", "")}
		}},
		ownerGatedProbe{"CleanupWorkspace", "CleanupWorkspace", func() proto.Message {
			return &leapmuxv1.CleanupWorkspaceRequest{}
		}},
		ownerGatedProbe{"GetTabPayload", "GetTabPayload", func() proto.Message {
			return &leapmuxv1.GetTabPayloadRequest{TabId: "tab-1"}
		}},
		ownerGatedProbe{"RevokeTabPayload", "RevokeTabPayload", func() proto.Message {
			return &leapmuxv1.RevokeTabPayloadRequest{TabId: "tab-1"}
		}},
		ownerGatedProbe{"ListAgents", "ListAgents", func() proto.Message {
			return &leapmuxv1.ListAgentsRequest{TabIds: []string{"agent-1"}}
		}},
		ownerGatedProbe{"ListTerminals", "ListTerminals", func() proto.Message {
			return &leapmuxv1.ListTerminalsRequest{TabIds: []string{"term-1"}}
		}},
		ownerGatedProbe{"WatchEvents", "WatchEvents", func() proto.Message {
			return &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
			}
		}},
		ownerGatedProbe{"ListAgentSessions", "ListAgentSessions", func() proto.Message {
			return &leapmuxv1.ListAgentSessionsRequest{
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				WorkingDir:    "/tmp",
			}
		}},

		// The machine-scoped families. Their reach is the whole filesystem
		// rather than one tab, so the populated request is where the denial
		// matters most: every one of these carries an absolute path, a target
		// address or a connection id that the handler must never read from a
		// stranger. TestMachineScopedFamiliesAreOwnerOnly dispatches them with
		// an EMPTY payload, which proves the gate refuses SOMETHING; these
		// prove it refuses a request that would otherwise have work to do.
		ownerGatedProbe{"GetGitInfo", "GetGitInfo", func() proto.Message {
			return &leapmuxv1.GetGitInfoRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"GetGitFileStatus", "GetGitFileStatus", func() proto.Message {
			return &leapmuxv1.GetGitFileStatusRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"ListGitBranches", "ListGitBranches", func() proto.Message {
			return &leapmuxv1.ListGitBranchesRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"ListGitWorktrees", "ListGitWorktrees", func() proto.Message {
			return &leapmuxv1.ListGitWorktreesRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"ReadGitFile", "ReadGitFile", func() proto.Message {
			return &leapmuxv1.ReadGitFileRequest{Path: "/tmp/x", Ref: leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD}
		}},
		ownerGatedProbe{"CheckoutBranch", "CheckoutBranch", func() proto.Message {
			return &leapmuxv1.CheckoutBranchRequest{Path: "/tmp", Branch: "main"}
		}},
		ownerGatedProbe{"CreateBranch", "CreateBranch", func() proto.Message {
			return &leapmuxv1.CreateBranchRequest{Path: "/tmp", NewBranch: "feature", BaseBranch: "main"}
		}},
		ownerGatedProbe{"DeleteBranch", "DeleteBranch", func() proto.Message {
			return &leapmuxv1.DeleteBranchRequest{Path: "/tmp", BranchToDelete: "feature", SwitchToBranch: "main"}
		}},
		ownerGatedProbe{"PushBranch", "PushBranch", func() proto.Message {
			return &leapmuxv1.PushBranchRequest{WorkingDir: "/tmp"}
		}},
		ownerGatedProbe{"InspectBranchChange", "InspectBranchChange", func() proto.Message {
			return &leapmuxv1.InspectBranchChangeRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"InspectBranchDeletion", "InspectBranchDeletion", func() proto.Message {
			return &leapmuxv1.InspectBranchDeletionRequest{Path: "/tmp", BranchNameHint: "feature"}
		}},
		ownerGatedProbe{"InspectWorktreeRemoval", "InspectWorktreeRemoval", func() proto.Message {
			return &leapmuxv1.InspectWorktreeRemovalRequest{Path: "/tmp"}
		}},
		ownerGatedProbe{"InspectLastTabClose", "InspectLastTabClose", func() proto.Message {
			return &leapmuxv1.InspectLastTabCloseRequest{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:   "agent-1",
			}
		}},
		ownerGatedProbe{"ListDirectory", "ListDirectory", func() proto.Message {
			return &leapmuxv1.ListDirectoryRequest{Path: "/tmp", MaxDepth: 1}
		}},
		ownerGatedProbe{"ReadFile", "ReadFile", func() proto.Message {
			return &leapmuxv1.ReadFileRequest{Path: "/tmp/x", Limit: 1}
		}},
		ownerGatedProbe{"StatFile", "StatFile", func() proto.Message {
			return &leapmuxv1.StatFileRequest{Path: "/tmp/x"}
		}},
		ownerGatedProbe{"ListAvailableShells", "ListAvailableShells", func() proto.Message {
			return &leapmuxv1.ListAvailableShellsRequest{}
		}},
		ownerGatedProbe{"ListAvailableProviders", "ListAvailableProviders", func() proto.Message {
			return &leapmuxv1.ListAvailableProvidersRequest{}
		}},
		ownerGatedProbe{"GetWorkerSystemInfo", "GetWorkerSystemInfo", func() proto.Message {
			return &leapmuxv1.GetWorkerSystemInfoRequest{}
		}},
		ownerGatedProbe{"OpenTunnelConn", "OpenTunnelConn", func() proto.Message {
			return &leapmuxv1.OpenTunnelConnRequest{
				ConnId: "conn-1", TargetAddr: "127.0.0.1", TargetPort: 9,
			}
		}},
		ownerGatedProbe{"SendTunnelData", "SendTunnelData", func() proto.Message {
			return &leapmuxv1.SendTunnelDataRequest{ConnId: "conn-1", Data: []byte("x"), Seq: 1}
		}},
		ownerGatedProbe{"CloseTunnelConn", "CloseTunnelConn", func() proto.Message {
			return &leapmuxv1.CloseTunnelConnRequest{ConnId: "conn-1", Seq: 2}
		}},
		ownerGatedProbe{"GrantTunnelReadCredit", "GrantTunnelReadCredit", func() proto.Message {
			return &leapmuxv1.GrantTunnelReadCreditRequest{ConnId: "conn-1", Credit: 4}
		}},
	)
	return probes
}()

// TestAccessControl_OwnerGatedProbesAreComplete is the check three comments in
// this file already claimed to perform, and did not.
//
// EVERY method the registrar puts behind the owner gate carries a typed denial
// probe, with no exemption list. A new owner-gated method therefore fails this
// test until its author writes one, which is what stops the next handler from
// reaching a stranger's populated request unproved. ListAgentSessions joined the
// gate with no probe and nothing failed, because the test the comments named had
// never been written.
func TestAccessControl_OwnerGatedProbesAreComplete(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	gates := registerAllWithGates(channel.NewDispatcher(), svc)

	probed := make(map[string]struct{}, len(ownerGatedProbes))
	for _, p := range ownerGatedProbes {
		probed[p.method] = struct{}{}
	}

	var unproved []string
	for method, gate := range gates {
		if gate != gateOwnerOnly {
			continue
		}
		if _, ok := probed[method]; !ok {
			unproved = append(unproved, method)
		}
	}
	sort.Strings(unproved)
	assert.Empty(t, unproved,
		"every owner-gated method needs a typed denial probe in ownerGatedProbes")

	// And no probe may outlive its gate: an entry for a method that nothing
	// registers, or one the registrar moved to another gate, passes forever
	// while proving nothing.
	for _, p := range ownerGatedProbes {
		assert.Equal(t, gateOwnerOnly, gates[p.method],
			"%s has a denial probe but is not an owner-gated method", p.method)
	}
}

// A caller who is NOT the worker's registrant must be refused by every one of
// these, with no row read and no response.
//
// The rows exist and the ids are real: the point is that ownership, not
// existence, is what decides. A probe that passed because the row was missing
// would prove nothing.
func TestAccessControl_OwnerGatedMethods_DenyOtherUser(t *testing.T) {
	t.Parallel()

	for _, tc := range ownerGatedProbes {
		t.Run(tc.name, func(t *testing.T) {
			svc, d, w := setupTestService(t)
			seedAgent(t, svc, "agent-1")
			seedTerminal(t, svc, "term-1")

			// "user-2" holds a valid channel but does not own this worker.
			dispatchAs(d, userid.MustNew("user-2"), tc.method, tc.req(), w)

			// rejections(), not w.errors: a streaming method reports its
			// denial as a stream frame, and the denial is what this asserts.
			rejected := w.rejections()
			require.Len(t, rejected, 1, "%s: expected one error", tc.name)
			assert.Equal(t, codePermissionDenied, rejected[0].code, "%s: expected PERMISSION_DENIED", tc.name)
			assert.Contains(t, rejected[0].message, "only the worker owner", "%s: denial should name the owner gate", tc.name)
			assert.Empty(t, w.responses, "%s: no response should be sent", tc.name)
		})
	}
}

// TestStreamingDenialArrivesAsAStreamFrame pins the SHAPE of a streaming
// method's refusal, which is this commit's headline gate change and which
// nothing asserted.
//
// TestMachineScopedFamiliesAreOwnerOnly already covers every owner-gated method
// denying a non-owner, but it checks through `rejections()`, which folds unary
// errors and error stream frames into one list -- so it passes whichever shape
// the gate emits. That is exactly the distinction that mattered: a streaming
// method whose gate answered unary left the browser holding a stream
// subscription with no End frame to terminate on.
//
// Asserted per streaming method, from the registration table, so a new streaming
// method is covered the moment it is registered.
func TestStreamingDenialArrivesAsAStreamFrame(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	_, shapes, _ := registerAllClassified(channel.NewDispatcher(), svc)

	var streaming []string
	for method, shape := range shapes {
		if shape == shapeStream {
			streaming = append(streaming, method)
		}
	}
	require.NotEmpty(t, streaming, "there must be streaming methods to check")

	for _, method := range streaming {
		t.Run(method, func(t *testing.T) {
			w := newTestWriter()
			d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-2")), &leapmuxv1.InnerRpcRequest{
				Method: method,
			}, w)

			assert.Empty(t, w.errors,
				"a streaming method must not answer a denial with a unary error frame")
			frames := w.streamsSnapshot()
			require.Len(t, frames, 1, "the denial must arrive as exactly one stream frame")
			assert.True(t, frames[0].GetIsError(), "the frame must be flagged as an error")
			assert.True(t, frames[0].GetEnd(),
				"and as terminal, so a receiver can end the stream on it generically")
			assert.Equal(t, int32(codePermissionDenied), frames[0].GetErrorCode())
		})
	}
}

// CleanupWorkspace takes the tab list the caller resolved before the hub
// dropped the workspace: the worker tracks no workspace id, so it cannot
// enumerate the set itself.
func TestCleanupWorkspace_ClosesTheNamedTabs(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	seedAgent(t, svc, "agent-untouched")

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{
		Tabs: []*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"},
		},
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the named agent should be soft-closed by cleanup")

	// A tab the caller did not name is untouched. That is not a gap: the list
	// is a best effort snapshot, and the orphan reconciler is what converges
	// anything it missed.
	other, err := svc.Queries.GetAgentByID(context.Background(), "agent-untouched")
	require.NoError(t, err)
	assert.False(t, other.ClosedAt.Valid, "cleanup must not close a tab the caller did not name")
}

// An empty tab list is a legitimate request (a workspace that held nothing on
// this worker), not an error, and must not be read as "close everything".
func TestCleanupWorkspace_EmptyTabListClosesNothing(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedAgent(t, svc, "agent-1")

	dispatch(d, "CleanupWorkspace", &leapmuxv1.CleanupWorkspaceRequest{}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "an empty list must close nothing")
}

// The machine-scoped families -- file, git, sysinfo, tunnel -- must admit ONLY
// the worker's registered owner.
//
// Their reach is the whole machine, not a workspace: validate.SanitizePath
// normalizes a path and blocks traversal, but does NOT confine it to a root, so
// any absolute path is fair game. That is fine for the owner (their agents already
// run as them on their own machine) and must be denied to everyone else -- above
// all a delegation bearer, which is handed to a prompt-injectable agent.
//
// Methods are enumerated from the gateOwnerOnly bucket of registerAllWithGates
// rather than by replaying the four family register functions. An empty payload
// suffices: ownerOnlyRegistrar.gate runs requireWorkerOwner BEFORE the handler
// unmarshals anything, so a non-owner is refused without a valid request ever
// being built -- which is itself the property worth pinning (an ungated handler
// would get as far as parsing attacker-supplied bytes).
//
// Tab-scoped methods sit behind the SAME gate, wired structurally via
// registerOwnerGated / registerAgentGated / registerTerminalGated (and the
// Tracked / ByID / ForRestart variants); their per-method denials are covered by
// ownerGatedProbes. Completeness is asserted by
// TestAccessControl_OwnerGatedProbesAreComplete and
// TestEveryRegisteredMethodIsClassified.
func TestMachineScopedFamiliesAreOwnerOnly(t *testing.T) {
	t.Parallel()

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
			d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-2")), &leapmuxv1.InnerRpcRequest{
				Method: method,
			}, w)

			// rejections(), not w.errors: a streaming method reports its
			// denial as a stream frame -- a unary reply on a correlation id the
			// client holds as a stream is dropped on arrival.
			rejected := w.rejections()
			require.Len(t, rejected, 1, "a non-owner must be refused")
			assert.Equal(t, codePermissionDenied, rejected[0].code)
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
	t.Parallel()

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
	t.Parallel()

	svc, d, _ := setupTestService(t)
	gates := registerAllWithGates(channel.NewDispatcher(), svc)

	var gated []string
	for method := range gates {
		gated = append(gated, method)
	}
	assert.ElementsMatch(t, d.Methods(), gated,
		"every method RegisterAll wires must have a recorded methodGate")

	var ungated []string
	for method, gate := range gates {
		if gate == gateNone {
			ungated = append(ungated, method)
		}
	}
	assert.ElementsMatch(t, []string{"Ping"}, ungated,
		"gateNone additions must be an explicit reviewed decision")
}

// TestEveryStreamingMethodIsRegisteredAsStreaming is the reply-shape
// companion to the gate check above.
//
// A method that answers with stream frames but is registered through a unary
// helper compiles and passes its own tests; it fails only in production. The
// browser holds the correlation id as a stream, so a unary reply carries no End
// for it to terminate on, and a non-error unary payload there is dropped
// outright. WatchWorkerPrivateEvents shipped that way.
//
// This list is the reviewed set: adding to it should be a decision, and losing
// an entry should be a red build. It does NOT catch a NEW method that streams
// but was registered unary -- `shapes` is derived from which helper was called,
// so it can only restate that choice. TestNoUnaryHandlerSendsStreamFrames below
// checks the direction this one cannot.
func TestEveryStreamingMethodIsRegisteredAsStreaming(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	_, shapes, _ := registerAllClassified(channel.NewDispatcher(), svc)

	var streaming []string
	for method, shape := range shapes {
		if shape == shapeStream {
			streaming = append(streaming, method)
		}
	}

	assert.ElementsMatch(t,
		[]string{"WatchEvents", "WatchWorkerPrivateEvents"}, streaming,
		"a method that answers with SendStream must be registered through a "+
			"streaming helper, so its panics and gate rejections reach the client "+
			"in the shape it is listening for")
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
	t.Parallel()

	svc := &Service{}
	svc.SetRegisteredBy(userid.MustNew("user-1"))

	const rounds = 200
	var wg sync.WaitGroup

	// The connect loop: re-delivers the owner on every reconnect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			svc.SetRegisteredBy(userid.MustNew("user-1"))
		}
	}()

	// Handlers left over from a previous connection, still gating.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				w := newTestWriter()
				requireWorkerOwner(svc, userid.MustNew("user-1"), w)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, "user-1", svc.RegisteredBy().String(), "the owner must survive concurrent access")
}

// An empty caller id must NOT match an empty RegisteredBy.
//
// MatchesUser fails closed when either side is zero -- so a worker whose
// RegisteredBy never got populated (the standalone path reads it from a state
// file with `omitempty` and, unlike solo mode, backfills nothing) refuses a
// caller the Hub named with an empty user id. A gate that exists to fail closed
// must not fail open on the one input it cannot judge.
func TestRequireWorkerOwnerRefusesEmptyIdentities(t *testing.T) {
	t.Parallel()

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
			svc := &Service{}
			svc.SetRegisteredBy(useridFromTest(tc.registeredBy))
			w := newTestWriter()
			assert.False(t, requireWorkerOwner(svc, useridFromTest(tc.userID), w),
				"an empty identity must never satisfy the owner gate")
			require.Len(t, w.errors, 1, "the refusal is reported to the caller")
			assert.Equal(t, codePermissionDenied, w.errors[0].code)
		})
	}
}

// ...and the owner keeps unrestricted reach, including outside the home directory.
// This is deliberate: the worker and its agents already have it.
func TestMachineScopedFamiliesAllowOwnerOutsideHome(t *testing.T) {
	t.Parallel()

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

// TestNoUnaryHandlerSendsStreamFrames checks the direction
// TestEveryStreamingMethodIsRegisteredAsStreaming structurally cannot.
//
// That test reads `shapes`, which registrar.register derives from the dispatch
// MODE -- i.e. from which helper the author called. So it restates the choice
// rather than verifying it, and a brand-new handler that calls SendStream while
// registered through a unary helper passes it. That is precisely the bug this
// commit fixed on WatchWorkerPrivateEvents, so the invariant needs a source of
// truth outside the registration call: what the handler body actually does.
//
// Parses this package and flags any registration whose handler reaches a
// stream-emitting call (SendStream / sendStreamError) while registered unary.
// Deliberately shallow -- it matches the handler function literal or the named
// function passed at the registration site, not an arbitrary call graph -- which
// is enough, because a handler that streams does so directly through its
// ResponseWriter.
func TestNoUnaryHandlerSendsStreamFrames(t *testing.T) {
	t.Parallel()

	// Parsed file by file rather than through parser.ParseDir, which is
	// deprecated (it ignores build tags when grouping files into packages).
	// Grouping is irrelevant here -- every non-test .go file in this directory is
	// package service by definition of the directory.
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, perr, "parsing %s", name)
		files = append(files, f)
	}
	require.NotEmpty(t, files, "the service package must have source files to scan")

	// Handler bodies that emit stream frames, by enclosing function name.
	streamers := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if x.Sel.Name == "SendStream" {
						streamers[fn.Name.Name] = true
					}
				case *ast.Ident:
					if x.Name == "sendStreamError" {
						streamers[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	require.Contains(t, streamers, "sendStreamError",
		"the detector must at minimum see sendStreamError's own body -- if it does not, it is matching nothing")

	// Every method registered through a UNARY helper, with why we believe its
	// handler streams: either the named function it passes is a known streamer, or
	// the inline literal it passes emits stream frames itself.
	offenders := map[string]string{}
	scanned := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := registrarHelperName(call)
			if !ok || streamingHelpers[name] {
				return true
			}
			method, handler, lit, ok := registrationMethodAndHandler(call)
			if !ok {
				return true
			}
			scanned++
			switch {
			case handler != "" && streamers[handler]:
				offenders[method] = "its handler " + handler
			// An inline literal is the common shape, and the streamers map cannot
			// see it: that scan attributes a literal's body to the ENCLOSING
			// function, not to the registration. Inspect it here instead --
			// missing this is what let the mutation "register
			// WatchWorkerPrivateEvents unary" slip past an earlier version.
			case lit != nil && emitsStreamFrames(lit):
				offenders[method] = "its inline handler"
			}
			return true
		})
	}
	require.Positive(t, scanned, "the scan must find unary registrations")

	for method, why := range offenders {
		assert.Fail(t,
			"streaming handler registered as unary",
			"%s is registered through a UNARY helper but %s emits stream frames; register it "+
				"with a streaming helper so its denials and panics reach the client in the shape "+
				"it is listening for", method, why)
	}
}

// emitsStreamFrames reports whether n's subtree calls SendStream or
// sendStreamError.
func emitsStreamFrames(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "SendStream" {
				found = true
			}
		case *ast.Ident:
			if x.Name == "sendStreamError" {
				found = true
			}
		}
		return !found
	})
	return found
}

// registrarHelperName returns the called registrar helper's name, if the call is
// one. Covers both the bare `registerX(...)` form and `d.registerX(...)`.
// streamingHelpers is the set of registration helpers that reach
// Dispatcher.RegisterStream, derived from the source rather than guessed from the
// helper's NAME.
//
// The name test it replaces ("does the helper contain 'Stream'?") was a string
// match standing in for a structural fact, and it constrained unrelated design:
// folding the streaming axis into a dispatchMode argument would have renamed the
// helper out of the match and silently reclassified a streaming registration as
// unary, flagging its legitimate SendStream. Deriving it means the classification
// follows the code.
var streamingHelpers = func() map[string]bool {
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, file := range []string{"registrar.go", "service.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			panic("streamingHelpers: parsing " + file + ": " + err.Error())
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var reachesStream, reachesUnary bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "RegisterStream":
					reachesStream = true
				case "Register", "RegisterTracked":
					reachesUnary = true
				}
				return true
			})
			// Streaming-ONLY. The low-level register() dispatches every mode, so it
			// reaches RegisterStream too -- but a call site going through it is not
			// thereby streaming, and treating it as such would skip a real unary
			// registration.
			if reachesStream && !reachesUnary {
				out[fn.Name.Name] = true
			}
		}
	}
	if len(out) == 0 {
		panic("streamingHelpers: found no helper reaching RegisterStream -- the scan is broken, not the code")
	}
	return out
}()

func registrarHelperName(call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if strings.HasPrefix(fn.Name, "register") {
			return fn.Name, true
		}
	case *ast.SelectorExpr:
		if strings.HasPrefix(fn.Sel.Name, "register") || strings.HasPrefix(fn.Sel.Name, "Register") {
			return fn.Sel.Name, true
		}
	case *ast.IndexExpr:
		// The generic helpers: registerOwnerGated[T, PT](...).
		if id, ok := fn.X.(*ast.Ident); ok && strings.HasPrefix(id.Name, "register") {
			return id.Name, true
		}
	case *ast.IndexListExpr:
		if id, ok := fn.X.(*ast.Ident); ok && strings.HasPrefix(id.Name, "register") {
			return id.Name, true
		}
	}
	return "", false
}

// registrationMethodAndHandler pulls the method-name literal out of a
// registration call, plus whichever handler form it passes: a named function, an
// inline literal, or a factory call. Returns ok=false for a call with no
// string-literal method.
func registrationMethodAndHandler(call *ast.CallExpr) (method, handler string, lit *ast.FuncLit, ok bool) {
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.BasicLit:
			if a.Kind == token.STRING && method == "" {
				method = strings.Trim(a.Value, `"`)
			}
		case *ast.Ident:
			handler = a.Name
		case *ast.FuncLit:
			lit = a
		case *ast.CallExpr:
			// e.g. handleCleanupWorkspace(svc) -- the factory IS the handler.
			if id, idOK := a.Fun.(*ast.Ident); idOK {
				handler = id.Name
			}
		}
	}
	return method, handler, lit, method != ""
}

// TestEveryRegisteredMethodIsWithinTheDelegationGrant pins the cross-worker
// half of the delegation ceiling: the channel a sibling worker opens
// authenticates with the delegation bearer the hub mints, and the gate below
// refuses any method whose declared scope the grant does not carry. When the
// ceiling carried only its hub-side scopes, every cross-worker inner call --
// a tab close, a file read, a branch push from a spawned agent -- answered
// PermissionDenied where it worked the day before. This test fails the suite
// the moment a new worker method states a scope the delegation grant lacks,
// which is the earliest point that breakage can be caught.
func TestEveryRegisteredMethodIsWithinTheDelegationGrant(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	gates, _, scopes := registerAllClassified(d, svc)

	// The delegation grant, spelled exactly as the hub's mint writes it
	// (worker_delegation_handler.go derives it from CeilingFor). The literal
	// here is a test PIN: a scope added to the worker surface without the
	// ceiling following fails right below, and a ceiling edit that drops a
	// scope the worker needs fails the same assertion from the other side.
	grant, err := authscope.Parse("workspace:read workspace:write worker:read " +
		"agent:read agent:write terminal:read terminal:write file:read git:read git:write tunnel:open")
	require.NoError(t, err)

	methods := d.Methods()
	require.NotEmpty(t, methods)
	for _, method := range methods {
		_, gated := gates[method]
		scope, stated := scopes[method]
		if !gated || !stated {
			// The ungated local probe records no scope; a sibling never sees
			// it, because the cross-worker path authenticates as a caller and
			// the gate reads the caller's grant.
			continue
		}
		token, _ := authscope.Token(scope)
		assert.Truef(t, grant.Allows(scope),
			"%s requires %s, which the delegation grant does not carry: a sibling-worker call answers PermissionDenied", method, token)
	}
}

// TestAccessControl_WorktreeRemoveNeedsGitWrite pins the action-level gate on
// RevokeTabPayload: the method rides file:read because the registry row is
// file-tab bookkeeping, but the WORKTREE_ACTION_REMOVE leg runs
// `git worktree remove --force` and `git branch -D` -- a destructive write
// that scope.proto places under git:write, the same family rule every other
// destructive verb follows. A read consent must never delete a worktree.
func TestAccessControl_WorktreeRemoveNeedsGitWrite(t *testing.T) {
	t.Parallel()

	owner := userid.MustNew("user-1")
	// file:read alone: what a read-only file-viewer consent holds. Its
	// closure carries worker:read, so the channel opens; it must not carry
	// git:write.
	readOnly, err := authscope.Parse("file:read")
	require.NoError(t, err)
	require.False(t, readOnly.Close().Allows(leapmuxv1.Scope_SCOPE_GIT_WRITE),
		"precondition: the read-only grant must not reach git:write")
	readWrite, err := authscope.Parse("file:read git:write")
	require.NoError(t, err)

	dispatch := func(scopes authscope.ScopeSet, action leapmuxv1.WorktreeAction) *testResponseWriter {
		svc, d, w := setupTestService(t)
		// Per-OS absolute paths: the store refuses a file path that is not
		// absolute on the running system, and a Unix literal is not one on
		// Windows.
		repo := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{
			UserID: owner.String(), TabID: "tab-1", Payload: fileTabPayload(filepath.Join(repo, "file"), repo),
		}))
		payload, err := proto.Marshal(&leapmuxv1.RevokeTabPayloadRequest{
			TabId: "tab-1", WorktreeAction: action,
		})
		require.NoError(t, err)
		d.DispatchWith(context.Background(), channel.NewCaller(owner, scopes), &leapmuxv1.InnerRpcRequest{
			Method: "RevokeTabPayload", Payload: payload,
		}, w)
		return w
	}

	// REMOVE under a read-only grant is refused before any row is touched.
	w := dispatch(readOnly, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE)
	rejected := w.rejections()
	require.Len(t, rejected, 1)
	assert.Equal(t, codePermissionDenied, rejected[0].code)
	assert.Contains(t, rejected[0].message, "git:write",
		"the denial should name the permission the escalation needs")

	// KEEP (the bookkeeping-only leg) stays reachable under file:read alone,
	// and REMOVE stays reachable when the grant carries git:write.
	w = dispatch(readOnly, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP)
	assert.Empty(t, w.rejections(), "revoking the registry row is file:read work")

	w = dispatch(readWrite, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE)
	assert.Empty(t, w.rejections(), "git:write covers worktree management")
	assert.NotEmpty(t, w.responses, "the removal proceeds and answers")
}
