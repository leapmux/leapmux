package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func decodeMessageContent(t *testing.T, content []byte, compression leapmuxv1.ContentCompression) string {
	t.Helper()
	raw, err := msgcodec.Decompress(content, compression)
	require.NoError(t, err)

	var body struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	return body.Content
}

func TestSendControlResponse_PersistsCodexUserInputAnswer(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "7",
		Payload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"item/tool/requestUserInput",
			"params":{
				"questions":[
					{"header":"Task","id":"task"},
					{"header":"Reason","id":"reason"}
				]
			}
		}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"result":{
				"answers":{
					"task":{"answers":["Inspect the renderer"]},
					"reason":{"answers":["Need parity with Claude Code"]}
				}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Task: Inspect the renderer\nReason: Need parity with Claude Code", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

func TestSendControlResponse_PersistsCodexFeedbackMessage(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "req-1",
		Payload: []byte(`{
			"type":"control_request",
			"request_id":"req-1",
			"request":{"tool_name":"ExitPlanMode"}
		}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"type":"control_response",
			"response":{
				"subtype":"success",
				"request_id":"req-1",
				"response":{
					"behavior":"deny",
					"message":"Please add tests before exiting plan mode."
				}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Please add tests before exiting plan mode.", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

// TestSendControlResponse_CodexPlanModePromptDenyFeedbackIsMarked covers the Codex
// plan-mode-prompt DENY-with-feedback path in the shared control-response planner. The
// user's typed rejection reason is their own answer to the control request, so it must draw
// a scroll-rail jump dot (CONTROL_RESPONSE) -- consistent with every other deny-with-
// feedback path -- rather than staying unmarked like a truly synthetic prompt.
func TestSendControlResponse_CodexPlanModePromptDenyFeedbackIsMarked(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"response":{
				"request_id":"plan-1",
				"response":{
					"behavior":"deny",
					"message":"Not yet -- split the migration first."
				}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Not yet -- split the migration first.", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"a plan-mode-prompt denial's typed feedback is the user's control answer and draws a rail dot")
}

// TestSendControlResponse_CodexPlanModePromptBareDenyPersistsRejectedRow covers the deny path
// when the user gave NO reason: the frontend auto-fills the ControlRejectedByUserMessage
// placeholder, which controlResponsePlan.rejectionMessage() (via agent.NormalizeRejectionMessage)
// collapses to "" -- so the planner persists the synthetic {controlResponse action:"rejected"}
// display row (LEAPMUX-sourced, still mark-carrying) rather than a USER-sourced feedback row
// echoing the placeholder as if it were the user's words.
func TestSendControlResponse_CodexPlanModePromptBareDenyPersistsRejectedRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// The sentinel has no JSON-special characters, so embedding the constant keeps the wire
	// value in lockstep with the backend's collapse rule without a fmt import.
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"deny","message":"` + agent.ControlRejectedByUserMessage + `"}}}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[0].Source,
		"a bare denial (placeholder collapsed to no feedback) persists the synthetic display row, not a user feedback row")
	action, comment := decodeControlResponseRow(t, rows[0].Content, rows[0].ContentCompression)
	assert.Equal(t, "rejected", action)
	assert.Empty(t, comment,
		"a bare denial must NOT leak the ControlRejectedByUserMessage placeholder as feedback -- the row renders \"Rejected\", not \"Sent feedback: Rejected by user.\"")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponse_CodexPlanModePromptAllowPersistsMarkedApproval(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       marshalOptions(map[string]string{agent.CodexOptionCollaborationMode: agent.CodexCollaborationDefault}),
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"response":{
				"request_id":"plan-1",
				"response":{"behavior":"allow"}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[0].Source)
	assert.Equal(t, "approved", decodeControlResponseAction(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"the user's plan-mode approval must have a scroll-rail control-response dot")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[1].Source)
	assert.Equal(t, "Implement the plan.", decodeMessageContent(t, rows[1].Content, rows[1].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED, rows[1].MarkType,
		"the auto-injected prompt is not the user's own answer")
}

func TestBuildControlResponsePlan_MalformedPlanPromptHasNoDecision(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	}))
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	plan := svc.buildControlResponsePlan("agent-1", dbAgent, []byte(`{"response":{"request_id":"plan-1"},"clearContext":true}`))

	assert.True(t, plan.requestMeta.Loaded)
	assert.Equal(t, agent.PlanModeControlPrompt, plan.resolution.PlanModeControl)
	assert.False(t, plan.hasDecision)
}

func TestBuildControlResponsePlan_WhitespacePaddedBehaviorStillDecides(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	}))
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// A behavior string with surrounding whitespace must still resolve to a decision: behavior()
	// trims to match ControlBehaviorAllow (mirroring agent.DecodeControlBehavior's trimming read),
	// so hasDecision holds and the plan-mode/answer-row handling is not silently skipped.
	plan := svc.buildControlResponsePlan("agent-1", dbAgent, []byte(`{"response":{"request_id":"plan-1","response":{"behavior":" allow "}}}`))

	assert.True(t, plan.requestMeta.Loaded)
	assert.Equal(t, agent.ControlBehaviorAllow, plan.behavior(), "behavior() trims surrounding whitespace")
	assert.True(t, plan.hasDecision, "a whitespace-padded allow still counts as a decision")
}

func TestSendControlResponse_BroadcastsCancelBeforeSyntheticMessage(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "req-1",
		Payload: []byte(`{
			"type":"control_request",
			"request_id":"req-1",
			"request":{"tool_name":"ExitPlanMode"}
		}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: "test-ch",
		Sender:    channel.NewSender(w),
	})

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"type":"control_response",
			"response":{
				"subtype":"success",
				"request_id":"req-1",
				"response":{
					"behavior":"deny",
					"message":"Please revise the plan."
				}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	var kinds []string
	for _, stream := range w.streamsSnapshot() {
		ev := decodeWatchAgentEvent(t, stream)
		switch ev.GetEvent().(type) {
		case *leapmuxv1.AgentEvent_ControlCancel:
			kinds = append(kinds, "cancel")
		case *leapmuxv1.AgentEvent_AgentMessage:
			kinds = append(kinds, "message")
		}
	}
	require.GreaterOrEqual(t, len(kinds), 2)
	assert.Equal(t, []string{"cancel", "message"}, kinds[:2])
}

func TestSendControlResponse_PersistsOpenCodeQuestionAnswer(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "que-1",
		Payload: []byte(`{
			"type":"question.asked",
			"properties":{
				"questions":[
					{"header":"Task","question":"Pick a task"},
					{"header":"Env","question":"Pick an environment"}
				]
			}
		}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"jsonrpc":"2.0",
			"id":"que-1",
			"result":{
				"answers":[["Build"],["Dev"]]
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Task: Build\nEnv: Dev", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

func TestSendControlResponse_PersistsCopilotPermissionSelectionLabel(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}))

	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "7",
		Payload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"requestPermission",
			"params":{
				"options":[
					{"optionId":"proceed_once","name":"Allow once","kind":"allow_once"},
					{"optionId":"cancel","name":"Deny","kind":"reject_once"}
				]
			}
		}`),
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "auto"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"result":{
				"outcome":{"outcome":"selected","optionId":"proceed_once"}
			}
		}`),
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Allow once", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

// decodeControlResponseAction decompresses a `{controlResponse:{action,comment}}` fallback
// display row and returns its action.
func decodeControlResponseAction(t *testing.T, content []byte, compression leapmuxv1.ContentCompression) string {
	t.Helper()
	action, _ := decodeControlResponseRow(t, content, compression)
	return action
}

// decodeControlResponseRow decompresses a `{controlResponse:{action,comment}}` display row and
// returns both fields. The comment must have the ControlRejectedByUserMessage placeholder collapsed
// to "" by persistControlResponseDisplayRow -- a bare deny renders "Rejected", not the placeholder.
func decodeControlResponseRow(t *testing.T, content []byte, compression leapmuxv1.ContentCompression) (action, comment string) {
	t.Helper()
	raw, err := msgcodec.Decompress(content, compression)
	require.NoError(t, err)
	var body struct {
		ControlResponse struct {
			Action  string `json:"action"`
			Comment string `json:"comment"`
		} `json:"controlResponse"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	return body.ControlResponse.Action, body.ControlResponse.Comment
}

// TestSendControlResponse_PersistsClaudePermissionFallbackRow covers the Claude permission
// path: the provider resolver produces no display text for Bash and Bash is not
// self-displaying, so the planner's fallback `{controlResponse}` row IS the display -- and
// must carry the CONTROL_RESPONSE mark.
func TestSendControlResponse_PersistsClaudePermissionFallbackRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[0].Source)
	assert.Equal(t, "approved", decodeControlResponseAction(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "the fallback row draws the rail jump dot")
}

// TestSendControlResponse_ClaudePermissionBareDenyRowHasNoComment covers the fallback-row deny
// path when the user gave NO reason: the frontend auto-fills the ControlRejectedByUserMessage
// placeholder, which persistControlResponseDisplayRow must collapse to "" (via
// plan.rejectionMessage) so the row renders "Rejected" -- NOT "Sent feedback: Rejected by user."
// The row previously stored the raw message, leaking the placeholder as if it were typed feedback.
func TestSendControlResponse_ClaudePermissionBareDenyRowHasNoComment(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"` + agent.ControlRejectedByUserMessage + `"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	action, comment := decodeControlResponseRow(t, rows[0].Content, rows[0].ContentCompression)
	assert.Equal(t, "rejected", action)
	assert.Empty(t, comment,
		"a bare denial must collapse the ControlRejectedByUserMessage placeholder to \"\" -- the row renders \"Rejected\", not \"Sent feedback: Rejected by user.\"")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestSendControlResponse_ClaudePermissionDenyWithReasonKeepsComment is the counterpart: a REAL
// typed reason (not the placeholder) must survive into the display row's comment, proving the
// normalization collapses only the sentinel and never a genuine rejection reason.
func TestSendControlResponse_ClaudePermissionDenyWithReasonKeepsComment(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"use ripgrep instead"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	action, comment := decodeControlResponseRow(t, rows[0].Content, rows[0].ContentCompression)
	assert.Equal(t, "rejected", action)
	assert.Equal(t, "use ripgrep instead", comment, "a genuine typed rejection reason must survive into the display row")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponse_UsesNestedRequestIDWhenTopLevelIDAlsoExists(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"id":"jsonrpc-req",
			"type":"control_response",
			"response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}
		}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "approved", decodeControlResponseAction(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponse_SkipsFallbackRowWithoutStoredControlRequest(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"missing","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rows, "without the stored request, the service cannot classify the answer row")
}

// TestSendControlResponse_SkipsClaudeSelfDisplayingFallbackRow asserts the fallback row is
// SKIPPED for a self-displaying tool (ExitPlanMode) -- its own tool_result carries the mark
// via ingestion, so a second synthetic `{controlResponse}` row would double the rail dot.
func TestSendControlResponse_SkipsClaudeSelfDisplayingFallbackRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// Approve WITHOUT clearContext, so no plan execution / synthetic prompt is persisted.
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	for _, row := range rows {
		assert.NotEqual(t, "approved", decodeControlResponseAction(t, row.Content, row.ContentCompression),
			"no synthetic {controlResponse} fallback row for a self-displaying tool")
	}
}

func TestSendControlResponse_RestoresClaudeSelfDisplayedToolUseType(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-ask",
		Payload: []byte(`{"type":"control_request","request_id":"req-ask","request":{"tool_name":"AskUserQuestion","tool_use_id":"toolu-ask"}}`),
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	sink.ResetSpans()
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-ask","response":{"behavior":"allow","message":"Yes, continue."}}}`),
	}, w)
	require.Empty(t, w.errors)

	assert.Equal(t, agent.ToolNameAskUserQuestion, sink.GetSpanType("toolu-ask"),
		"the later Claude tool_result relies on this mapping to carry the CONTROL_RESPONSE mark")
}

func TestSendControlResponse_ClaudeExitPlanModeClearContextMarksFallbackRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode","tool_use_id":"toolu-exit"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","clearContext":true,"response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)

	controlRows := make([]db.Message, 0, len(rows))
	for _, row := range rows {
		if decodeControlResponseAction(t, row.Content, row.ContentCompression) != "" {
			controlRows = append(controlRows, row)
		}
	}

	require.Len(t, controlRows, 1)
	assert.Equal(t, "approved", decodeControlResponseAction(t, controlRows[0].Content, controlRows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, controlRows[0].MarkType,
		"clear-context skips Claude's self-displayed tool_result, so the fallback row must carry the rail dot")
}

// TestSendControlResponse_EnterPlanModeAllowTrimsRequestID pins the "trim everywhere" rule for
// the plan-mode transition: a control response whose request_id carries surrounding whitespace
// must still switch the agent to plan mode. plan.requestMeta.RequestID is trimmed (it comes
// through DecodeControlBehavior), so applyControlResponsePlanModeEffects must trim the response's
// own request_id the same way -- otherwise the untrimmed " req-1 " != "req-1" equality would
// silently skip the transition even though hasDecision (which trims) accepted it.
func TestSendControlResponse_EnterPlanModeAllowTrimsRequestID(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"EnterPlanMode","tool_use_id":"toolu-enter"}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// The request_id carries surrounding whitespace on the wire.
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"  req-1  ","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	got := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, agent.PermissionModePlan, got[agent.OptionIDPermissionMode],
		"a whitespace-padded request_id must still apply the EnterPlanMode transition")
}

// TestSendControlResponse_CursorCreatePlanPersistsOnlySyntheticAnswerRow covers the Cursor
// createPlan path, where the provider REWRITES resolution.Content into an ACP outcome that
// carries no request_id while the ORIGINAL response content still carries the frontend
// approve/reject envelope. buildControlResponsePlan decodes plan.decision from that original
// content, so hasDecision is TRUE here -- which makes the fallback-row gate
// (needsFallbackDisplayRow) actually evaluate for this path rather than short-circuit on
// !hasDecision. The answer must route to the single synthetic {content} row (marked
// CONTROL_RESPONSE for a rail dot) and MUST NOT also emit a {controlResponse} fallback row --
// i.e. exactly one persisted row, never two.
func TestSendControlResponse_CursorCreatePlanPersistsOnlySyntheticAnswerRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "7",
		Payload: []byte(`{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`),
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"response":{"request_id":"7","response":{"behavior":"deny","message":"Needs tests."}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1", Seq: 0, Limit: 10,
	})
	require.NoError(t, err)
	// Exactly one row: the synthetic answer. A second row would be the {controlResponse}
	// fallback double-rendering now that hasDecision is reachable for this path.
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source,
		"the answer is the synthetic {content} user row, not a LEAPMUX {controlResponse} fallback row")
	assert.Equal(t, "Needs tests.", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"a control-response answer draws a scroll-rail jump dot")
}

// TestSendControlResponse_CursorCreatePlanApprovePersistsOnlySyntheticAnswerRow is the ALLOW
// sibling of the deny case above. Approving createPlan flips hasDecision true (decoded from the
// original content), so applyControlResponsePlanModeEffects now runs its `behavior == Allow`
// branch for this path -- which must be a NO-OP because Cursor's createPlan is PlanModeControlNone
// (no permission-mode switch, no plan execution) -- while the answer still routes to the single
// synthetic "Accept" row with no {controlResponse} fallback.
func TestSendControlResponse_CursorCreatePlanApprovePersistsOnlySyntheticAnswerRow(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "plan-7",
		Payload: []byte(`{"jsonrpc":"2.0","id":"plan-7","method":"cursor/create_plan","params":{}}`),
	}))
	before, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	_, err = svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"response":{"request_id":"plan-7","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1", Seq: 0, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly the synthetic answer row -- no {controlResponse} fallback double-render")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Accept", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)

	// createPlan is PlanModeControlNone, so the Allow branch must not touch permission mode.
	after, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before.Options, after.Options, "approving createPlan must not change agent options/permission mode")
}

func TestControlResponseRequestID(t *testing.T) {
	// Claude Code format: response.request_id
	assert.Equal(t, "req-1", controlResponseRequestID(
		[]byte(`{"response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
	))

	// Mixed envelopes still belong to the nested control response. A top-level
	// JSON-RPC id can be present for provider plumbing, but the pending LeapMux
	// control_request row is keyed by response.request_id.
	assert.Equal(t, "req-1", controlResponseRequestID(
		[]byte(`{"id":"jsonrpc-req","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
	))

	// OpenCode / ACP JSON-RPC format: numeric id
	assert.Equal(t, "5", controlResponseRequestID(
		[]byte(`{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"once"}}}`),
	))

	// OpenCode / ACP JSON-RPC format: string id
	assert.Equal(t, "abc-123", controlResponseRequestID(
		[]byte(`{"jsonrpc":"2.0","id":"abc-123","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`),
	))

	// No request ID
	assert.Equal(t, "", controlResponseRequestID(
		[]byte(`{"type":"unknown"}`),
	))

	// Null id
	assert.Equal(t, "", controlResponseRequestID(
		[]byte(`{"id":null}`),
	))

	// Invalid JSON
	assert.Equal(t, "", controlResponseRequestID(
		[]byte(`not json`),
	))
}

// TestClassifyControlAnswerRow pins the single home for the "which row carries the control
// answer's rail mark" decision that the synthetic-row and fallback-row persist sites share.
// Exactly one of the three homes owns the mark for any provider-resolved self-display flag
// and displayText, so the two sites can't both mark (a double dot) or both skip (a real
// answer with no dot).
func TestClassifyControlAnswerRow(t *testing.T) {
	cases := []struct {
		name          string
		selfDisplayed bool
		displayText   string
		want          controlAnswerRow
	}{
		// The future-proofing invariant: self-display is checked FIRST, so a self-displaying
		// tool classifies as self-displayed even WITH display text -- the synthetic row stays
		// unmarked and can't double the ingested tool_result's dot. (No provider is in this
		// state today; this pins the precedence so a new one can't break it.)
		{"self-display without display text", true, "", controlAnswerSelfDisplayed},
		{"self-display wins over display text (no double dot)", true, "answered", controlAnswerSelfDisplayed},
		// A Claude permission decision self-displays nothing and carries no display text: the
		// fallback {controlResponse} row is its home.
		{"non-self-displayed empty text -> fallback row", false, "", controlAnswerFallback},
		// A non-self-displaying tool WITH display text -> the synthetic answer row owns the mark.
		{"non-self-displayed text -> synthetic row", false, "approved", controlAnswerSynthetic},
		// A whitespace-only displayText is treated as empty (mirrors persistSyntheticUserMessage's
		// own TrimSpace guard), so it falls through to the fallback rather than a blank synthetic row.
		{"whitespace-only display text -> fallback row", false, "   \n\t ", controlAnswerFallback},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyControlAnswerRow(tc.selfDisplayed, tc.displayText))
		})
	}
}

func TestExitPlanClearingContext(t *testing.T) {
	// The single triple that both needsFallbackDisplayRow and the plan-mode-effects skipSend
	// key off: an APPROVED ExitPlanMode that ALSO clears context (the transcript row is wiped,
	// so the fallback display row carries the mark and the approval is not forwarded).
	mk := func(behavior string, ctrl agent.PlanModeControlKind, clear bool) controlResponsePlan {
		var plan controlResponsePlan
		plan.decision.Response.Response.Behavior = behavior
		plan.decision.ClearContext = clear
		plan.resolution.PlanModeControl = ctrl
		return plan
	}
	assert.True(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlExit, true).exitPlanClearingContext())
	assert.False(t, mk(agent.ControlBehaviorDeny, agent.PlanModeControlExit, true).exitPlanClearingContext(), "deny is not an approval")
	assert.False(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlEnter, true).exitPlanClearingContext(), "enter is not exit")
	assert.False(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlExit, false).exitPlanClearingContext(), "not clearing context")
}

func TestResolveTargetMode(t *testing.T) {
	// The frontend's attached permission mode wins when present.
	assert.Equal(t, agent.PermissionModePlan, resolveTargetMode(agent.PermissionModePlan, agent.PermissionModeDefault))
	// Empty falls back to the caller's default -- the ONE thing the plan-prompt path
	// (PermissionModeDefault) and the ExitPlanMode path (PermissionModeAcceptEdits) differ on.
	assert.Equal(t, agent.PermissionModeDefault, resolveTargetMode("", agent.PermissionModeDefault))
	assert.Equal(t, agent.PermissionModeAcceptEdits, resolveTargetMode("", agent.PermissionModeAcceptEdits))
}
