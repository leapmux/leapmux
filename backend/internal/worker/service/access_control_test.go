package service

import (
	"context"
	"errors"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/rpcaccess"
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
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         agentID,
		WorkingDir: t.TempDir(),
	}))
}

func seedTerminal(t *testing.T, svc *Service, terminalID string) {
	t.Helper()
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         terminalID,
		WorkingDir: t.TempDir(),
		Screen:     []byte{},
	}))
}

// agentHandlerCases enumerates the agent-ID-scoped handlers restricted via
// requireAccessibleAgent. Each entry builds the request proto for a given
// agent ID and returns the RPC method name to dispatch.
type agentHandlerCase struct {
	method string
	req    func(agentID string) proto.Message
}

var agentHandlerCases = []agentHandlerCase{
	{leapmuxv1connect.WorkerTabServiceCloseTabProcedure, func(id string) proto.Message {
		return &leapmuxv1.CloseTabRequest{Tab: agentTabIdent(id)}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.EnqueueAgentInputRequest{
			InputId: newTestAgentInputID(),
			Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, AgentId: id, Text: "hello"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure, func(id string) proto.Message {
		return &leapmuxv1.BeginQueuedAgentInputEditRequest{AgentId: id, InputId: "input", ClientId: "client"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.UpdateQueuedAgentInputRequest{AgentId: id, InputId: "input", ClientId: "client"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceCancelQueuedAgentInputEditProcedure, func(id string) proto.Message {
		return &leapmuxv1.CancelQueuedAgentInputEditRequest{AgentId: id, InputId: "input", ClientId: "client"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.DeleteQueuedAgentInputRequest{AgentId: id, InputId: "input"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceMoveQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.MoveQueuedAgentInputRequest{AgentId: id, InputId: "input"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceSetAgentInputQueuePausedProcedure, func(id string) proto.Message {
		return &leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: id, Paused: true}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.SteerQueuedAgentInputRequest{AgentId: id, InputId: "input"}
	}},
	{leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: id, InputId: "input"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceRetryQueuedAgentInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.RetryQueuedAgentInputRequest{AgentId: id, InputId: "input"}
	}},
	{leapmuxv1connect.WorkerAgentServiceSendAgentRawMessageProcedure, func(id string) proto.Message {
		return &leapmuxv1.SendAgentRawMessageRequest{AgentId: id, Content: "{}"}
	}},
	{leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure, func(id string) proto.Message {
		return &leapmuxv1.RenameAgentRequest{AgentId: id, Title: "renamed"}
	}},
	{leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, func(id string) proto.Message {
		return &leapmuxv1.ListAgentInputQueueRequest{AgentId: id}
	}},
	{leapmuxv1connect.WorkerAgentServiceUpdateAgentSettingsProcedure, func(id string) proto.Message {
		return &leapmuxv1.UpdateAgentSettingsRequest{AgentId: id, Settings: &leapmuxv1.AgentSettings{Options: map[string]string{"model": "opus"}}}
	}},
	{leapmuxv1connect.WorkerAgentServiceSendControlResponseProcedure, func(id string) proto.Message {
		return &leapmuxv1.SendControlResponseRequest{AgentId: id, Content: []byte("{}")}
	}},
	{leapmuxv1connect.WorkerAgentServiceUpdateAgentGoalProcedure, func(id string) proto.Message {
		return &leapmuxv1.UpdateAgentGoalRequest{
			AgentId:   id,
			Action:    leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR,
			Objective: "",
		}
	}},
	{leapmuxv1connect.WorkerAgentServiceApproveAutoReviewDenialProcedure, func(id string) proto.Message {
		return &leapmuxv1.ApproveAutoReviewDenialRequest{AgentId: id, MessageId: "message-1", ReviewId: "review-1"}
	}},
	{leapmuxv1connect.WorkerAgentServiceRetryAgentTurnProcedure, func(id string) proto.Message {
		return &leapmuxv1.RetryAgentTurnRequest{AgentId: id, TurnId: "turn-1", Model: "gpt-6-luna"}
	}},
	{leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, func(id string) proto.Message {
		return &leapmuxv1.ListAgentMessagesRequest{AgentId: id}
	}},
	{leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure, func(id string) proto.Message {
		return &leapmuxv1.GetAgentMessageRequest{AgentId: id, Seq: 1}
	}},
	{leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure, func(id string) proto.Message {
		return &leapmuxv1.GetAgentSpanMessagesRequest{AgentId: id, SpanId: "call"}
	}},
	{leapmuxv1connect.WorkerAgentServiceListMessageMarksProcedure, func(id string) proto.Message {
		return &leapmuxv1.ListMessageMarksRequest{AgentId: id}
	}},
	// InterruptAgent is agent-ID-scoped via registerAgentGuarded.
	{leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, func(id string) proto.Message {
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
	{leapmuxv1connect.WorkerTabServiceCloseTabProcedure, func(id string) proto.Message {
		return &leapmuxv1.CloseTabRequest{Tab: terminalTabIdent(id)}
	}},
	{leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, func(id string) proto.Message {
		return &leapmuxv1.RestartTerminalRequest{TerminalId: id, Cols: 80, Rows: 25}
	}},
	{leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, func(id string) proto.Message {
		return &leapmuxv1.SendInputRequest{TerminalId: id, Data: []byte("x")}
	}},
	{leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, func(id string) proto.Message {
		return &leapmuxv1.ResizeTerminalRequest{TerminalId: id, Cols: 80, Rows: 25}
	}},
	{leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure, func(id string) proto.Message {
		return &leapmuxv1.RenameTerminalRequest{TerminalId: id, Title: "renamed"}
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
// cover both the simple "look up row" path (RenameAgent, RenameTerminal)
// and the "use returned row" path that exercises the second return value
// (ListAgentMessages).

func TestAccessControl_AgentHandlers_HappyPath(t *testing.T) {
	t.Parallel()

	t.Run("RenameAgent", func(t *testing.T) {
		svc, d, w := setupTestService(t)
		seedAgent(t, svc, "agent-1")

		dispatch(d, leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure, &leapmuxv1.RenameAgentRequest{
			AgentId: "agent-1",
			Title:   "Renamed",
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})

	t.Run("ListAgentMessages", func(t *testing.T) {
		svc, d, w := setupTestService(t)
		seedAgent(t, svc, "agent-1")

		dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{AgentId: "agent-1"}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
	})
}

func TestAccessControl_TerminalHandlers_HappyPath(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedTerminal(t, svc, "term-1")

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure, &leapmuxv1.RenameTerminalRequest{
		TerminalId: "term-1",
		Title:      "New Title",
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}

// ownerGuardedProbe describes one non-owner denial probe for a method the
// registrar wires behind the owner gate. Completeness is enforced by
// TestAccessControl_OwnerGuardedProbesAreComplete against registerAllWithGuards.
//
// The gate replaced here was the per-workspace one. It is gone because there is
// nothing left for it to narrow: a Worker is registered by exactly one user and
// stores no workspace id, so "the caller owns this Worker" and "the caller owns
// every row it holds" are the same statement. What still has to be proved, and
// is proved below, is that EVERY method is behind that one gate -- an unguarded
// handler would parse and act on a stranger's request.
type ownerGuardedProbe struct {
	name   string
	method string
	req    func() proto.Message
}

// ownerGuardedProbes covers every guardOwnerOnly method that takes a typed request
// with at least one non-owner denial. Derived entries reuse agentHandlerCases /
// terminalHandlerCases; the residue is hand-written.
var ownerGuardedProbes = func() []ownerGuardedProbe {
	var probes []ownerGuardedProbe
	for _, tc := range agentHandlerCases {
		probes = append(probes, ownerGuardedProbe{
			name:   tc.method,
			method: tc.method,
			req:    func() proto.Message { return tc.req("agent-1") },
		})
	}
	for _, tc := range terminalHandlerCases {
		probes = append(probes, ownerGuardedProbe{
			name:   tc.method,
			method: tc.method,
			req:    func() proto.Message { return tc.req("term-1") },
		})
	}
	probes = append(probes,
		ownerGuardedProbe{leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, func() proto.Message {
			return &leapmuxv1.OpenAgentRequest{WorkingDir: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, func() proto.Message {
			return &leapmuxv1.OpenTerminalRequest{WorkingDir: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, func() proto.Message {
			return &leapmuxv1.WatchWorkerPrivateEventsRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, func() proto.Message {
			return &leapmuxv1.SetQuakePanelRequest{
				WorkingDir: "/tmp",
				Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE,
			}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, func() proto.Message {
			return &leapmuxv1.RegisterTabPayloadRequest{TabId: "tab-1", Payload: fileTabPayload("/tmp/x", "")}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, func() proto.Message {
			return &leapmuxv1.CleanupWorkspaceRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, func() proto.Message {
			return &leapmuxv1.GetTabPayloadRequest{TabId: "tab-1"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTabServiceCloseTabProcedure, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, func() proto.Message {
			return &leapmuxv1.CloseTabRequest{Tab: fileTabIdent("tab-1")}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, func() proto.Message {
			return &leapmuxv1.ListAgentsRequest{TabIds: []string{"agent-1"}}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, func() proto.Message {
			return &leapmuxv1.ListTerminalsRequest{TabIds: []string{"term-1"}}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, func() proto.Message {
			return &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
			}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerAgentServiceListAgentSessionsProcedure, leapmuxv1connect.WorkerAgentServiceListAgentSessionsProcedure, func() proto.Message {
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
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, func() proto.Message {
			return &leapmuxv1.GetGitInfoRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, func() proto.Message {
			return &leapmuxv1.GetGitFileStatusRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, func() proto.Message {
			return &leapmuxv1.ListGitBranchesRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, func() proto.Message {
			return &leapmuxv1.ListGitWorktreesRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, func() proto.Message {
			return &leapmuxv1.ReadGitFileRequest{Path: "/tmp/x", Ref: leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure, leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure, func() proto.Message {
			return &leapmuxv1.CheckoutBranchRequest{Path: "/tmp", Branch: "main"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceCreateBranchProcedure, leapmuxv1connect.WorkerGitServiceCreateBranchProcedure, func() proto.Message {
			return &leapmuxv1.CreateBranchRequest{Path: "/tmp", NewBranch: "feature", BaseBranch: "main"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure, leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure, func() proto.Message {
			return &leapmuxv1.DeleteBranchRequest{Path: "/tmp", BranchToDelete: "feature", SwitchToBranch: "main"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServicePushBranchProcedure, leapmuxv1connect.WorkerGitServicePushBranchProcedure, func() proto.Message {
			return &leapmuxv1.PushBranchRequest{WorkingDir: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceInspectBranchChangeProcedure, leapmuxv1connect.WorkerGitServiceInspectBranchChangeProcedure, func() proto.Message {
			return &leapmuxv1.InspectBranchChangeRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, func() proto.Message {
			return &leapmuxv1.InspectBranchDeletionRequest{Path: "/tmp", BranchNameHint: "feature"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, func() proto.Message {
			return &leapmuxv1.InspectWorktreeRemovalRequest{Path: "/tmp"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure, leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure, func() proto.Message {
			return &leapmuxv1.InspectLastTabCloseRequest{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, func() proto.Message {
			return &leapmuxv1.ListDirectoryRequest{Path: "/tmp", MaxDepth: 1}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerFileServiceListFilesystemRootsProcedure, leapmuxv1connect.WorkerFileServiceListFilesystemRootsProcedure, func() proto.Message {
			return &leapmuxv1.ListFilesystemRootsRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerFileServiceReadFileProcedure, leapmuxv1connect.WorkerFileServiceReadFileProcedure, func() proto.Message {
			return &leapmuxv1.ReadFileRequest{Path: "/tmp/x", Limit: 1}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerFileServiceStatFileProcedure, leapmuxv1connect.WorkerFileServiceStatFileProcedure, func() proto.Message {
			return &leapmuxv1.StatFileRequest{Path: "/tmp/x"}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, func() proto.Message {
			return &leapmuxv1.InspectTerminalProcessesRequest{TerminalIds: []string{"term-1"}}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, func() proto.Message {
			return &leapmuxv1.ListAvailableShellsRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, func() proto.Message {
			return &leapmuxv1.ListAvailableProvidersRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, func() proto.Message {
			return &leapmuxv1.GetWorkerSystemInfoRequest{}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure, leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure, func() proto.Message {
			return &leapmuxv1.OpenTunnelConnRequest{
				ConnId: "conn-1", TargetAddr: "127.0.0.1", TargetPort: 9,
			}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTunnelServiceSendTunnelDataProcedure, leapmuxv1connect.WorkerTunnelServiceSendTunnelDataProcedure, func() proto.Message {
			return &leapmuxv1.SendTunnelDataRequest{ConnId: "conn-1", Data: []byte("x"), Seq: 1}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTunnelServiceCloseTunnelConnProcedure, leapmuxv1connect.WorkerTunnelServiceCloseTunnelConnProcedure, func() proto.Message {
			return &leapmuxv1.CloseTunnelConnRequest{ConnId: "conn-1", Seq: 2}
		}},
		ownerGuardedProbe{leapmuxv1connect.WorkerTunnelServiceGrantTunnelReadCreditProcedure, leapmuxv1connect.WorkerTunnelServiceGrantTunnelReadCreditProcedure, func() proto.Message {
			return &leapmuxv1.GrantTunnelReadCreditRequest{ConnId: "conn-1", Credit: 4}
		}},
	)
	return probes
}()

// TestAccessControl_OwnerGuardedProbesAreComplete is the check three comments in
// this file already claimed to perform, and did not.
//
// EVERY method the registrar puts behind the owner gate carries a typed denial
// probe, with no exemption list. A new owner-gated method therefore fails this
// test until its author writes one, which is what stops the next handler from
// reaching a stranger's populated request unproved. ListAgentSessions joined the
// gate with no probe and nothing failed, because the test the comments named had
// never been written.
func TestAccessControl_OwnerGuardedProbesAreComplete(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	gates := registerAllWithGuards(channel.NewDispatcher(), svc)

	probed := make(map[string]struct{}, len(ownerGuardedProbes))
	for _, p := range ownerGuardedProbes {
		probed[p.method] = struct{}{}
	}

	var unproved []string
	for method, gate := range gates {
		if gate != guardOwnerOnly {
			continue
		}
		if _, ok := probed[method]; !ok {
			unproved = append(unproved, method)
		}
	}
	sort.Strings(unproved)
	assert.Empty(t, unproved,
		"every owner-gated method needs a typed denial probe in ownerGuardedProbes")

	// And no probe may outlive its gate: an entry for a method that nothing
	// registers, or one the registrar moved to another gate, passes forever
	// while proving nothing.
	for _, p := range ownerGuardedProbes {
		assert.Equal(t, guardOwnerOnly, gates[p.method],
			"%s has a denial probe but is not an owner-gated method", p.method)
	}
}

// A caller who is NOT the worker's registrant must be refused by every one of
// these, with no row read and no response.
//
// The rows exist and the ids are real: the point is that ownership, not
// existence, is what decides. A probe that passed because the row was missing
// would prove nothing.
func TestAccessControl_OwnerGuardedMethods_DenyOtherUser(t *testing.T) {
	t.Parallel()

	for _, tc := range ownerGuardedProbes {
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
	_, shapes := registerAllClassified(channel.NewDispatcher(), svc)

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
			assert.NotNil(t, frames[0].GetError(), "the frame must be flagged as an error")
			assert.True(t, frames[0].GetEnd(),
				"and as terminal, so a receiver can end the stream on it generically")
			assert.Equal(t, int32(codePermissionDenied), frames[0].GetError().GetCode())
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

	dispatch(d, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, &leapmuxv1.CleanupWorkspaceRequest{
		Tabs: []*leapmuxv1.TabIdent{
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

	dispatch(d, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, &leapmuxv1.CleanupWorkspaceRequest{}, w)

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
// Methods are enumerated from the guardOwnerOnly bucket of registerAllWithGuards
// rather than by replaying the four family register functions. An empty payload
// suffices: ownerOnlyRegistrar.gate runs callerIsWorkerOwner BEFORE the handler
// unmarshals anything, so a non-owner is refused without a valid request ever
// being built -- which is itself the property worth pinning (an ungated handler
// would get as far as parsing attacker-supplied bytes).
//
// Tab-scoped methods sit behind the SAME gate, wired structurally via
// registerOwnerGuarded / registerAgentGuarded / registerTerminalGuarded (and the
// Tracked / ByID / ForRestart variants); their per-method denials are covered by
// ownerGuardedProbes. Completeness is asserted by
// TestAccessControl_OwnerGuardedProbesAreComplete and
// TestEveryRegisteredMethodIsClassified.
func TestMachineScopedFamiliesAreOwnerOnly(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	gates := registerAllWithGuards(channel.NewDispatcher(), svc)

	var methods []string
	for method, gate := range gates {
		if gate == guardOwnerOnly {
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

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, w)

	require.Empty(t, w.errors, "the owner must pass the owner-only gate")
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAvailableShellsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetShells(), "owner should see at least one installed shell")
}

// TestEveryRegisteredMethodIsClassified is the default-deny companion: EVERY
// method registerAllWithGuards wires must appear in the gate map, the wired set
// must be exactly the methods the protos declare on WORKER services, and the
// open-by-design bucket is pinned with an explicit list so additions are
// reviewed decisions. Disjointness (no method recorded twice) is enforced by
// the registrar's and the dispatcher's duplicate panics at registration time.
func TestEveryRegisteredMethodIsClassified(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	gates := registerAllWithGuards(channel.NewDispatcher(), svc)

	var gated []string
	for method := range gates {
		gated = append(gated, method)
	}
	assert.ElementsMatch(t, d.Methods(), gated,
		"every method RegisterAll wires must have a recorded methodGate")

	var declared []string
	for _, m := range rpcaccess.Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER) {
		declared = append(declared, m.Procedure)
	}
	assert.ElementsMatch(t, declared, d.Methods(),
		"RegisterAll must serve exactly the methods the protos declare on WORKER services")

	var ungated []string
	for method, gate := range gates {
		if gate == gateNone {
			ungated = append(ungated, method)
		}
	}
	assert.ElementsMatch(t, []string{leapmuxv1connect.WorkerSystemServicePingProcedure}, ungated,
		"gateNone additions must be an explicit reviewed decision")
}

// TestRegisteredShapesFollowTheDeclarations pins that the registrar registers
// each method in the reply shape its proto declares: a server-streaming method
// as streaming, every other method as unary.
func TestRegisteredShapesFollowTheDeclarations(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	_, shapes := registerAllClassified(channel.NewDispatcher(), svc)
	require.NotEmpty(t, shapes)
	streaming := 0
	for method, shape := range shapes {
		declared, ok := rpcaccess.Lookup(method)
		require.Truef(t, ok, "%s is registered but not declared", method)
		want := shapeUnary
		if declared.ServerStreaming {
			want = shapeStream
			streaming++
		}
		assert.Equalf(t, want, shape, "%s is registered in a shape its declaration does not state", method)
	}
	assert.Positive(t, streaming, "no streaming method at all; the assertion proved nothing")
}

// TestAStreamingMethodDeniesWithAStreamFrame pins every refusal of a
// server-streaming method -- a decode failure, a scope denial and an owner
// denial -- to one terminal stream frame, because the client holds the
// correlation id as a stream and a unary error there carries no End.
func TestAStreamingMethodDeniesWithAStreamFrame(t *testing.T) {
	t.Parallel()

	const method = leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure
	_, d, _ := setupTestService(t)
	for _, tc := range []struct {
		name   string
		caller channel.Caller
		body   []byte
		code   codes.Code
	}{
		{"decode failure", channel.LocalAgentCaller(userid.MustNew("user-1")), []byte{0xff}, codes.InvalidArgument},
		{"scope denial", channel.NewCaller(userid.MustNew("user-1"), mustScopes("worker:read terminal:read")), nil, codes.PermissionDenied},
		{"owner denial", channel.LocalAgentCaller(userid.MustNew("user-2")), nil, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWriter()
			d.DispatchWith(context.Background(), tc.caller, &leapmuxv1.InnerRpcRequest{Method: method, Payload: tc.body}, w)
			assert.Empty(t, w.errors, "a streaming method must not refuse with a unary error")
			frames := w.streamsSnapshot()
			require.Len(t, frames, 1)
			assert.NotNil(t, frames[0].GetError())
			assert.True(t, frames[0].GetEnd())
			assert.Equal(t, int32(tc.code), frames[0].GetError().GetCode())
		})
	}
}

// TestAUnaryMethodDeniesWithAUnaryError is the other half: a unary method's
// decode failure, scope denial and owner denial each arrive as one unary error
// and never as a stream frame.
func TestAUnaryMethodDeniesWithAUnaryError(t *testing.T) {
	t.Parallel()

	const method = leapmuxv1connect.WorkerAgentServiceListAgentSessionsProcedure
	_, d, _ := setupTestService(t)
	for _, tc := range []struct {
		name   string
		caller channel.Caller
		body   []byte
		code   codes.Code
	}{
		{"decode failure", channel.LocalAgentCaller(userid.MustNew("user-1")), []byte{0xff}, codes.InvalidArgument},
		{"scope denial", channel.NewCaller(userid.MustNew("user-1"), mustScopes("worker:read terminal:read")), nil, codes.PermissionDenied},
		{"owner denial", channel.LocalAgentCaller(userid.MustNew("user-2")), nil, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWriter()
			d.DispatchWith(context.Background(), tc.caller, &leapmuxv1.InnerRpcRequest{Method: method, Payload: tc.body}, w)
			assert.Empty(t, w.streamsSnapshot(), "a unary method must not refuse with a stream frame")
			require.Len(t, w.errors, 1)
			assert.Equal(t, int32(tc.code), w.errors[0].code)
		})
	}
}

// The owner is written by the connect loop and read by handlers on their own
// goroutines, so the two genuinely race and the field must be atomic.
//
// Within ONE connection the DispatchAsync goroutine spawn orders them. A RECONNECT
// does not: Manager.CloseAll cancels session contexts WITHOUT waiting for in-flight
// handlers, so a handler from the previous connection can still be inside
// the owner gate while the next connection's receive loop delivers WorkerIdentity
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
	gate := newRegistrar(channel.NewDispatcher(), svc).ownerOnly().gate(
		leapmuxv1connect.WorkerFileServiceStatFileProcedure,
		func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				gate(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{}, newTestWriter())
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
func TestOwnerGateRefusesEmptyIdentities(t *testing.T) {
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
			reached := false
			gate := newRegistrar(channel.NewDispatcher(), svc).ownerOnly().gate(
				leapmuxv1connect.WorkerFileServiceStatFileProcedure,
				func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
					reached = true
				})
			w := newTestWriter()
			gate(context.Background(), channel.LocalAgentCaller(useridFromTest(tc.userID)), &leapmuxv1.InnerRpcRequest{}, w)
			assert.False(t, reached, "an empty identity must never satisfy the owner gate")
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

	dispatch(d, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, w)
	require.Empty(t, w.errors, "the owner must not be refused")
	require.Len(t, w.responses, 1)

	// An absolute path outside HomeDir still resolves for the owner. Use the
	// parent of HomeDir rather than a hard-coded path like /etc: it is always
	// absolute, always exists, and is outside the home on every GOOS, so the
	// test does not regress on Windows where /etc fails SanitizePath.
	outsideHome := filepath.Dir(svc.HomeDir)
	w2 := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerFileServiceStatFileProcedure, &leapmuxv1.StatFileRequest{Path: outsideHome}, w2)
	require.Empty(t, w2.errors, "the owner may stat outside their home directory")
}

// TestNoUnaryHandlerSendsStreamFrames checks that no handler of a method the
// proto declares unary emits stream frames.
//
// The registrar takes a method's reply shape from its declaration, so a
// registration can no longer pick the wrong shape. What it cannot see is the
// handler body: a unary method whose handler calls SendStream compiles and
// passes its own tests, and fails only in production, because the client holds
// the correlation id as a unary request. So the invariant needs a source of
// truth outside the registration call: what the handler body actually does.
//
// Parses this package and flags any registration of a unary method whose
// handler reaches a stream-emitting call (SendStream / sendStreamError).
// Deliberately shallow -- it matches the handler function literal, the named
// function or method value passed at the registration site, not an arbitrary
// call graph -- which is enough, because a handler that streams does so
// directly through its ResponseWriter.
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

	// The declared worker methods, by their leapmuxv1connect constant name.
	declared := map[string]rpcaccess.Method{}
	for _, m := range rpcaccess.Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER) {
		declared[rpcaccess.ConstName(m.Procedure)] = m
	}

	// Handler bodies that emit stream frames, by enclosing function name.
	streamers := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if emitsStreamFrames(fn.Body) {
				streamers[fn.Name.Name] = true
			}
		}
	}
	require.Contains(t, streamers, "sendStreamError",
		"the detector must at minimum see sendStreamError's own body -- if it does not, it is matching nothing")

	// Every registration of a UNARY method, with why we believe its handler
	// streams: either the named function it passes is a known streamer, or the
	// inline literal it passes emits stream frames itself.
	offenders := map[string]string{}
	scanned := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if _, ok := registrarHelperName(call); !ok {
				return true
			}
			constName, handler, lit, ok := registrationMethodAndHandler(call)
			if !ok {
				return true
			}
			method, isDeclared := declared[constName]
			require.Truef(t, isDeclared, "a registration passes leapmuxv1connect.%s, which no WORKER service declares", constName)
			if method.ServerStreaming {
				return true
			}
			scanned++
			switch {
			case handler != "" && streamers[handler]:
				offenders[method.Procedure] = "its handler " + handler
			// An inline literal is the common shape, and the streamers map cannot
			// see it: that scan attributes a literal's body to the ENCLOSING
			// function, not to the registration. Inspect it here instead.
			case lit != nil && emitsStreamFrames(lit):
				offenders[method.Procedure] = "its inline handler"
			}
			return true
		})
	}
	require.Positive(t, scanned, "the scan must find unary registrations")

	for method, why := range offenders {
		assert.Fail(t,
			"unary method's handler emits stream frames",
			"%s is declared unary but %s emits stream frames; declare it server-streaming in the proto "+
				"or answer with a unary response", method, why)
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
// one. Covers both the bare `registerX(...)` form and `d.Register(...)`.
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
		// The generic helpers: registerOwnerGuarded[T, PT](...).
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

// registrationMethodAndHandler pulls the leapmuxv1connect procedure constant out
// of a registration call, plus whichever handler form it passes: a named
// function, a method value (tunnels.openConn), an inline literal, or a factory
// call. Returns ok=false for a call that passes no procedure constant.
func registrationMethodAndHandler(call *ast.CallExpr) (constName, handler string, lit *ast.FuncLit, ok bool) {
	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.SelectorExpr:
			if pkg, isIdent := a.X.(*ast.Ident); isIdent && pkg.Name == "leapmuxv1connect" {
				if constName == "" {
					constName = a.Sel.Name
				}
				continue
			}
			// A method value such as tunnels.openConn: the method IS the handler.
			handler = a.Sel.Name
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
	return constName, handler, lit, constName != ""
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

	svc, _, _ := setupTestService(t)
	d := channel.NewDispatcher()
	gates, _ := registerAllClassified(d, svc)

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
		require.Truef(t, gated, "%s is wired without a recorded gate", method)
		declared, ok := rpcaccess.Lookup(method)
		require.Truef(t, ok, "%s is wired but not declared", method)
		scope := declared.Access.GetScope()
		token, _ := authscope.Token(scope)
		assert.Truef(t, grant.Allows(scope),
			"%s requires %s, which the delegation grant does not carry: a sibling-worker call answers PermissionDenied", method, token)
	}
}

// TestAccessControl_WorktreeRemoveNeedsGitWrite pins the action-level gate on
// CloseTab: WORKTREE_ACTION_REMOVE runs `git worktree remove --force` and
// `git branch -D` -- a destructive write that scope.proto places under
// git:write, the same family rule every other destructive verb follows. The
// scope that closes a tab of each type (agent:write, terminal:write, file:read)
// never carries that permission, so a REMOVE close of ANY tab type needs
// git:write as well, and a KEEP close does not.
func TestAccessControl_WorktreeRemoveNeedsGitWrite(t *testing.T) {
	t.Parallel()

	owner := userid.MustNew("user-1")
	// Each grant is closed the way the mint closes it, so it carries the
	// worker:read that CloseTab's method gate requires.
	closedGrant := func(t *testing.T, raw string) authscope.ScopeSet {
		t.Helper()
		scopes, err := authscope.Parse(raw)
		require.NoError(t, err)
		return scopes.Close()
	}

	cases := []struct {
		name      string
		closeWith string
		// seed creates the tab and returns its identity.
		seed func(t *testing.T, svc *Service, tabID string) *leapmuxv1.TabIdent
		// open reports whether the tab is still open, which proves that a
		// denied close changed nothing.
		open func(t *testing.T, svc *Service, tabID string) bool
	}{
		{
			name:      "agent",
			closeWith: "agent:write",
			seed: func(t *testing.T, svc *Service, tabID string) *leapmuxv1.TabIdent {
				seedAgent(t, svc, tabID)
				return agentTabIdent(tabID)
			},
			open: func(t *testing.T, svc *Service, tabID string) bool {
				row, err := svc.Queries.GetAgentByID(context.Background(), tabID)
				require.NoError(t, err)
				return !row.ClosedAt.Valid
			},
		},
		{
			name:      "terminal",
			closeWith: "terminal:write",
			seed: func(t *testing.T, svc *Service, tabID string) *leapmuxv1.TabIdent {
				seedTerminal(t, svc, tabID)
				return terminalTabIdent(tabID)
			},
			open: func(t *testing.T, svc *Service, tabID string) bool {
				row, err := svc.Queries.GetTerminal(context.Background(), tabID)
				require.NoError(t, err)
				return !row.ClosedAt.Valid
			},
		},
		{
			// file:read alone: what a read-only file-viewer consent holds.
			name:      "file",
			closeWith: "file:read",
			seed: func(t *testing.T, svc *Service, tabID string) *leapmuxv1.TabIdent {
				// Per-OS absolute paths: the store refuses a file path that is
				// not absolute on the running system, and a Unix literal is not
				// one on Windows.
				repo := filepath.Join(t.TempDir(), "repo")
				require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{
					UserID: owner.String(), TabID: tabID, Payload: fileTabPayload(filepath.Join(repo, "file"), repo),
				}))
				return fileTabIdent(tabID)
			},
			open: func(t *testing.T, svc *Service, tabID string) bool {
				_, err := svc.TabPayloads.TabTypeOf(context.Background(), owner.String(), tabID)
				if errors.Is(err, ErrTabPayloadNotFound) {
					return false
				}
				require.NoError(t, err)
				return true
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			withoutGitWrite := closedGrant(t, tc.closeWith)
			require.False(t, withoutGitWrite.Allows(leapmuxv1.Scope_SCOPE_GIT_WRITE),
				"precondition: the grant that closes the tab must not reach git:write")
			withGitWrite := closedGrant(t, tc.closeWith+" git:write")

			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			closeTab := func(scopes authscope.ScopeSet, tab *leapmuxv1.TabIdent, action leapmuxv1.WorktreeAction) *testResponseWriter {
				payload, err := proto.Marshal(&leapmuxv1.CloseTabRequest{Tab: tab, WorktreeAction: action})
				require.NoError(t, err)
				w := newTestWriter()
				d.DispatchWith(context.Background(), channel.NewCaller(owner, scopes), &leapmuxv1.InnerRpcRequest{
					Method: leapmuxv1connect.WorkerTabServiceCloseTabProcedure, Payload: payload,
				}, w)
				return w
			}

			// REMOVE without git:write is refused before the tab is touched.
			removed := tc.seed(t, svc, "tab-remove")
			w := closeTab(withoutGitWrite, removed, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE)
			rejected := w.rejections()
			require.Len(t, rejected, 1)
			assert.Equal(t, codePermissionDenied, rejected[0].code)
			assert.Contains(t, rejected[0].message, "git:write",
				"the denial should name the permission the escalation needs")
			assert.True(t, tc.open(t, svc, "tab-remove"), "a denied close must leave the tab open")

			// The same grant closes the tab when it keeps the worktree...
			kept := tc.seed(t, svc, "tab-keep")
			w = closeTab(withoutGitWrite, kept, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP)
			assert.Empty(t, w.rejections(), "a KEEP close needs only the scope of the tab type")
			assert.NotEmpty(t, w.responses)
			assert.False(t, tc.open(t, svc, "tab-keep"), "the KEEP close closes the tab")

			// ...and git:write lets the REMOVE close proceed.
			w = closeTab(withGitWrite, removed, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE)
			assert.Empty(t, w.rejections(), "git:write covers worktree management")
			assert.NotEmpty(t, w.responses, "the removal proceeds and answers")
			assert.False(t, tc.open(t, svc, "tab-remove"), "the allowed REMOVE close closes the tab")
		})
	}
}

// CloseTab requires only worker:read at the method, because the scope a close
// needs depends on the tab type. Each case checks that the handler then
// requires the scope of the per-type close RPC it replaced: agent:write for an
// AGENT tab, terminal:write for a TERMINAL tab, file:read for a FILE tab. The
// grants are exact sets, so each one holds worker:read and lacks only the
// scope under test.
func TestAccessControl_CloseTabRequiresTheScopeOfItsTabType(t *testing.T) {
	t.Parallel()

	owner := userid.MustNew("user-1")
	grant := func(t *testing.T, raw string) authscope.ScopeSet {
		t.Helper()
		scopes, err := authscope.Parse(raw)
		require.NoError(t, err)
		return scopes
	}
	closeTab := func(t *testing.T, svc *Service, d *channel.Dispatcher, scopes authscope.ScopeSet, tab *leapmuxv1.TabIdent) *testResponseWriter {
		t.Helper()
		payload, err := proto.Marshal(&leapmuxv1.CloseTabRequest{Tab: tab})
		require.NoError(t, err)
		w := newTestWriter()
		d.DispatchWith(context.Background(), channel.NewCaller(owner, scopes), &leapmuxv1.InnerRpcRequest{
			Method: leapmuxv1connect.WorkerTabServiceCloseTabProcedure, Payload: payload,
		}, w)
		return w
	}

	t.Run("an agent close without agent:write is denied", func(t *testing.T) {
		t.Parallel()
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)
		seedAgent(t, svc, "agent-1")

		w := closeTab(t, svc, d, grant(t, "worker:read terminal:write"), agentTabIdent("agent-1"))
		require.Len(t, w.errors, 1)
		assert.Equal(t, codePermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "agent:write")
		row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
		require.NoError(t, err)
		assert.False(t, row.ClosedAt.Valid, "a denied close must not stamp closed_at")

		w = closeTab(t, svc, d, grant(t, "worker:read agent:write"), agentTabIdent("agent-1"))
		assert.Empty(t, w.errors, "agent:write closes an agent tab")
		require.Len(t, w.responses, 1)
	})

	t.Run("a terminal close with terminal:write is allowed", func(t *testing.T) {
		t.Parallel()
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)
		seedTerminal(t, svc, "term-1")

		w := closeTab(t, svc, d, grant(t, "worker:read agent:write"), terminalTabIdent("term-1"))
		require.Len(t, w.errors, 1)
		assert.Equal(t, codePermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal:write")

		w = closeTab(t, svc, d, grant(t, "worker:read terminal:write"), terminalTabIdent("term-1"))
		assert.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		row, err := svc.Queries.GetTerminal(context.Background(), "term-1")
		require.NoError(t, err)
		assert.True(t, row.ClosedAt.Valid, "the allowed close stamps closed_at")
	})

	t.Run("a file close without file:read is denied", func(t *testing.T) {
		t.Parallel()
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)
		repo := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, svc.TabPayloads.Register(context.Background(), RegisterTabPayloadParams{
			UserID: owner.String(), TabID: "file-1", Payload: fileTabPayload(filepath.Join(repo, "file"), repo),
		}))

		w := closeTab(t, svc, d, grant(t, "worker:read agent:write"), fileTabIdent("file-1"))
		require.Len(t, w.errors, 1)
		assert.Equal(t, codePermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "file:read")
		_, err := svc.TabPayloads.TabTypeOf(context.Background(), owner.String(), "file-1")
		require.NoError(t, err, "a denied close must not revoke the payload row")

		w = closeTab(t, svc, d, grant(t, "worker:read file:read"), fileTabIdent("file-1"))
		assert.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		_, err = svc.TabPayloads.TabTypeOf(context.Background(), owner.String(), "file-1")
		require.ErrorIs(t, err, ErrTabPayloadNotFound, "the allowed close revokes the payload row")
	})
}
