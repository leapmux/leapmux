package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
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

// decodedControlResponse keeps independent wire field names in the storage assertions.
type decodedControlResponse struct {
	Provider   string
	RequestID  string
	ClaimToken string
	Request    json.RawMessage
	Response   json.RawMessage
}

func decodeStructuredControlResponse(t *testing.T, row db.Message) decodedControlResponse {
	t.Helper()
	raw, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	require.NoError(t, err)
	supplement, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	require.NoError(t, err)
	var fields struct {
		Provider json.RawMessage `json:"provider"`
		Metadata struct {
			RequestID  string `json:"control_request_id"`
			ClaimToken string `json:"control_request_claim_token"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(supplement, &fields))
	return decodedControlResponse{
		Provider:   strings.TrimPrefix(row.AgentProvider.String(), "AGENT_PROVIDER_"),
		RequestID:  fields.Metadata.RequestID,
		ClaimToken: fields.Metadata.ClaimToken,
		Request:    fields.Provider,
		Response:   raw,
	}
}

func persistControlResponseForTest(t *testing.T, svc *Service, agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	t.Helper()
	content, err := controlResponseMessageContent(plan)
	require.NoError(t, err)
	require.NoError(t, svc.Output.persistAndBroadcast(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content,
		agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE}, nil))
}

func persistControlResponseAnswerForTest(t *testing.T, svc *Service, agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) {
	t.Helper()
	if plan.needsTranscriptRow() {
		persistControlResponseForTest(t, svc, agentID, provider, plan)
	}
}

// controlResponseRows returns the CONTROL_RESPONSE-marked rows, isolating the single structured
// answer row from any async plan-execution rows a clear-context path may append.
func controlResponseRows(rows []db.Message) []db.Message {
	marked := make([]db.Message, 0, len(rows))
	for _, row := range rows {
		if row.MarkType == leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE {
			marked = append(marked, row)
		}
	}
	return marked
}

func TestSendControlResponse_PersistsCodexUserInputRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
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
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{
		"jsonrpc":"2.0",
		"id":7,
		"result":{
			"answers":{
				"task":{"answers":["Inspect the renderer"]},
				"reason":{"answers":["Need parity with Claude Code"]}
			}
		}
	}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
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
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CODEX", cr.Provider)
	assert.Equal(t, "7", cr.RequestID)
	assert.JSONEq(t, `{
		"jsonrpc":"2.0","id":7,
		"method":"item/tool/requestUserInput",
		"params":{"questions":[{"id":"task","header":"Task"},{"id":"reason","header":"Reason"}]}
	}`, string(cr.Request), "the complete request retains the native identity and question labels")
	assert.Equal(t, string(response), string(cr.Response), "the native answer payload is retained verbatim")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

func TestControlResponseRejectsAnUnansweredOlderInstance(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	first := agent.ControlRequest{RequestID: "7", Payload: []byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"command":"first"}}`)}
	require.NoError(t, sink.PublishControlRequest(first))
	old, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "7"})
	require.NoError(t, err)
	second := agent.ControlRequest{RequestID: "7", Payload: []byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"command":"second"}}`)}
	require.NoError(t, sink.PublishControlRequest(second))
	current, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "7"})
	require.NoError(t, err)
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	_, forward, responseErr := processControlResponseForTest(svc, "agent-1", row, []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`), old.ClaimToken)
	require.NoError(t, responseErr)
	assert.False(t, forward)
	remaining, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "7"})
	require.NoError(t, err)
	assert.Equal(t, current.ClaimToken, remaining.ClaimToken)
	assert.Equal(t, second.Payload, remaining.Payload)
}

func TestSendControlResponse_PersistsCodexDenyFeedbackRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "req-1",
		Payload: []byte(`{
			"type":"control_request",
			"request_id":"req-1",
			"request":{"tool_name":"ExitPlanMode"}
		}`),
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{
		"type":"control_response",
		"response":{
			"subtype":"success",
			"request_id":"req-1",
			"response":{
				"behavior":"deny",
				"message":"Please add tests before exiting plan mode."
			}
		}
	}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
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
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CODEX", cr.Provider)
	assert.Equal(t, "req-1", cr.RequestID)
	// The typed feedback lives inside the native response; the frontend extracts and renders it.
	assert.Equal(t, string(response), string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

// TestSendControlResponse_CodexPlanModePromptDenyFeedbackIsMarked covers the Codex
// plan-mode-prompt DENY-with-feedback path. The user's typed rejection reason is their own answer,
// forwarded to the agent as a real user message that draws a scroll-rail jump dot -- it stays the
// plain {content} shape, not the structured control-response row.
func TestSendControlResponse_CodexPlanModePromptDenyFeedbackIsMarked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "plan-session"}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", AgentSessionID: "plan-session",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		ResumeSessionID: "plan-session",
		AgentID:         "agent-1",
		Options:         map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir:      t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
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

	rows := waitForMessageCount(t, svc, "agent-1", 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, "Not yet -- split the migration first.", decodeMessageContent(t, rows[0].Content, rows[0].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"a plan-mode-prompt denial's typed feedback is the user's control answer and draws a rail dot")
}

// TestSendControlResponse_CodexPlanModePromptBareDenyPersistsStructuredRow covers the plan-prompt
// deny path when the user gave NO reason: the frontend auto-fills the ControlRejectedByUserMessage
// placeholder, so there is no typed feedback to forward as a user message. The planner persists the
// structured control-response row instead, retaining the native response verbatim (the frontend --
// not the backend -- collapses the placeholder when rendering).
func TestSendControlResponse_CodexPlanModePromptBareDenyPersistsStructuredRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// The sentinel has no JSON-special characters, so embedding the constant keeps the wire
	// value in lockstep with the backend's rule without a fmt import.
	response := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"deny","message":"` + agent.ControlRejectedByUserMessage + `"}}}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
	}, w)

	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source,
		"a bare denial persists the structured control-response row (USER-sourced), identified by its {controlResponse} shape rather than a forwarded {content} feedback row")
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CODEX", cr.Provider)
	assert.JSONEq(t, `{"request":{"tool_name":"CodexPlanModePrompt"}}`, string(cr.Request))
	// The native response is retained verbatim -- the placeholder is not collapsed backend-side;
	// the frontend collapses it to render "Rejected".
	assert.Equal(t, string(response), string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponse_CodexPlanModePromptAllowPersistsMarkedApproval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       marshalOptions(map[string]string{contracts.CodexOptionCollaborationMode: codex.CollaborationDefault}),
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "plan-session"}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentSessionID: "plan-session",
		AgentID:        "agent-1",
		RequestID:      "plan-1",
		Payload:        []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		ResumeSessionID: "plan-session",
		AgentID:         "agent-1",
		Options:         map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir:      t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	confirmPlanCollaborationForTest(t, svc)

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

	rows := waitForMessageCount(t, svc, "agent-1", 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CODEX", cr.Provider)
	assert.JSONEq(t, `{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`, string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"the user's plan-mode approval must have a scroll-rail control-response dot")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[1].Source)
	assert.Equal(t, "Implement the plan.", decodeMessageContent(t, rows[1].Content, rows[1].ContentCompression))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED, rows[1].MarkType,
		"the auto-injected prompt is not the user's own answer")
}

func TestSendControlResponse_CodexPlanModePromptBypassAppliesAllSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options: marshalOptions(map[string]string{
			agent.OptionIDPermissionMode:           codex.DefaultApprovalPolicy,
			contracts.CodexOptionSandboxPolicy:     codex.SandboxWorkspaceWrite,
			contracts.CodexOptionNetworkAccess:     codex.NetworkRestricted,
			contracts.CodexOptionCollaborationMode: codex.CollaborationPlan,
		}),
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{
			"response":{"request_id":"plan-1","response":{"behavior":"allow"}}
		}`),
		PlanApproval: &leapmuxv1.PlanApprovalSettings{PermissionMode: "never"},
	}, w)
	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	options := loadOptions(testRegistry, dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	assert.Equal(t, "never", options[agent.OptionIDPermissionMode])
	assert.Equal(t, codex.NetworkEnabled, options[contracts.CodexOptionNetworkAccess])
	assert.Equal(t, codex.SandboxDangerFullAccess, options[contracts.CodexOptionSandboxPolicy])
	assert.Equal(t, codex.CollaborationDefault, options[contracts.CodexOptionCollaborationMode])
}

// TestSendControlResponse_CodexPlanModePromptDuplicateAnswerAppliesOnce pins the plan-prompt side of
// the idempotency claim: a plan-prompt answer processed twice -- with the request re-stored so BOTH
// answers resolve as a LOADED plan-prompt, mirroring a true concurrent retry rather than the
// request-gone path -- runs handleControlResponsePromptPlan (which persists the structured row AND
// applies plan-mode side effects like the "Implement the plan." prompt) exactly ONCE, not per retry.
func TestSendControlResponse_CodexPlanModePromptDuplicateAnswerAppliesOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       marshalOptions(map[string]string{contracts.CodexOptionCollaborationMode: codex.CollaborationDefault}),
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "plan-session"}))
	storeRequest := func() {
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentSessionID: "plan-session",
			AgentID:        "agent-1", RequestID: "plan-1",
			Payload: []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		})
	}
	storeRequest()
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		ResumeSessionID: "plan-session",
		AgentID:         "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	answer := &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`),
	}
	confirmPlanCollaborationForTest(t, svc)

	dispatch(d, "SendControlResponse", answer, w)
	// The first answer deleted the request; re-store it so the duplicate ALSO resolves as a loaded
	// plan-prompt. The idempotency claim -- not a request-gone short-circuit -- must stop the reapply.
	storeRequest()
	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors)

	rows := waitForMessageCount(t, svc, "agent-1", 2)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
	assert.Equal(t, "Implement the plan.", decodeMessageContent(t, rows[1].Content, rows[1].ContentCompression))
}

func TestBuildControlResponsePlan_MalformedPlanPromptHasNoDecision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	plan := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, []byte(`{"response":{"request_id":"plan-1"},"clearContext":true}`))

	assert.True(t, plan.requestMeta.Loaded)
	assert.Equal(t, agent.PlanModeControlPrompt, plan.resolution.PlanModeControl)
	assert.False(t, plan.hasDecision)
}

func TestBuildControlResponsePlan_WhitespacePaddedBehaviorStillDecides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// A behavior string with surrounding whitespace must still resolve to a decision: the envelope is
	// normalized at construction (mirroring agent.DecodeControlBehavior's trimming read), so behavior()
	// reads the trimmed value, hasDecision holds, and the plan-mode handling is not silently skipped.
	plan := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, []byte(`{"response":{"request_id":"plan-1","response":{"behavior":" allow "}}}`))

	assert.True(t, plan.requestMeta.Loaded)
	assert.Equal(t, agent.ControlBehaviorAllow, plan.behavior(), "behavior() reads the value trimmed at construction")
	assert.True(t, plan.hasDecision, "a whitespace-padded allow still counts as a decision")
}

// TestBuildControlResponsePlan_RequestIDNormalizedConsistently pins that the response envelope's
// request_id is trimmed ONCE at construction, so the hasDecision gate and the plan-mode mutation
// request-id match read the SAME normalized value and can never disagree. A padded-but-nonempty id
// trims and still decides (matching the trimmed requestMeta id); an all-whitespace id normalizes to
// "" and is treated as no-decision on BOTH gates (previously hasDecision read it untrimmed as
// non-empty while the mutation gate trimmed it to "" and skipped -- the inconsistency this closes).
func TestBuildControlResponsePlan_RequestIDNormalizedConsistently(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "plan-1",
		Payload:   []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// A padded-but-nonempty request_id trims to the loaded id, so it decides and its plan-mode
	// mutation gate would match (both read "plan-1").
	padded := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, []byte(`{"response":{"request_id":" plan-1 ","response":{"behavior":"allow"}}}`))
	assert.Equal(t, "plan-1", padded.decision.Response.RequestID, "request_id is trimmed at construction")
	assert.True(t, padded.hasDecision, "a padded-nonempty request_id still counts as a decision")

	// An all-whitespace request_id normalizes to "" -> no decision, consistently on both gates (the
	// hasDecision gate no longer reads it untrimmed-nonempty while the mutation gate trims it to "").
	blank := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, []byte(`{"response":{"request_id":"   ","response":{"behavior":"allow"}}}`))
	assert.Equal(t, "", blank.decision.Response.RequestID, "an all-whitespace request_id normalizes to empty")
	assert.False(t, blank.hasDecision, "an all-whitespace request_id is no decision on the hasDecision gate too")
}

func TestSendControlResponse_BroadcastsCancelBeforeSyntheticMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID:   "agent-1",
		RequestID: "req-1",
		Payload: []byte(`{
			"type":"control_request",
			"request_id":"req-1",
			"request":{"tool_name":"ExitPlanMode"}
		}`),
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	registerAgentWatch(svc, "test-ch", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

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

func TestSendControlResponse_PersistsOpenCodeQuestionRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
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
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{
		"jsonrpc":"2.0",
		"id":"que-1",
		"result":{
			"answers":[["Build"],["Dev"]]
		}
	}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
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
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "OPENCODE", cr.Provider)
	assert.Equal(t, "que-1", cr.RequestID)
	assert.JSONEq(t, `{
		"type":"question.asked",
		"properties":{"questions":[{"header":"Task","question":"Pick a task"},{"header":"Env","question":"Pick an environment"}]}
	}`, string(cr.Request), "the question headers let the frontend label the structured answers")
	assert.Equal(t, string(response), string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

func TestSendControlResponse_PersistsCopilotPermissionSelectionRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
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
	})

	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "auto"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{
		"jsonrpc":"2.0",
		"id":7,
		"result":{
			"outcome":{"outcome":"selected","optionId":"proceed_once"}
		}
	}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
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
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "GITHUB_COPILOT", cr.Provider)
	assert.JSONEq(t, `{
		"jsonrpc":"2.0","id":7,
		"method":"requestPermission",
		"params":{"options":[
			{"optionId":"proceed_once","name":"Allow once","kind":"allow_once"},
			{"optionId":"cancel","name":"Deny","kind":"reject_once"}
		]}
	}`, string(cr.Request), "the complete permission options retain their native values and labels")
	assert.Equal(t, string(response), string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "a control-response answer draws a scroll-rail jump dot")
}

// TestSendControlResponse_PersistsClaudePermissionRow covers the Claude permission path: Bash is
// not self-displaying, so the planner's structured control-response row IS the answer and must
// carry the CONTROL_RESPONSE mark.
func TestSendControlResponse_PersistsClaudePermissionRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CLAUDE_CODE", cr.Provider)
	assert.Equal(t, "req-1", cr.RequestID)
	assert.JSONEq(t, `{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`, string(cr.Request))
	assert.Equal(t, string(response), string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "the structured row draws the rail jump dot")
}

// TestSendControlResponse_ClaudePermissionBareDenyRetainsNativeResponse covers the deny path when
// the user gave NO reason: the frontend auto-fills the ControlRejectedByUserMessage placeholder,
// which is now RETAINED verbatim inside the native response (the frontend collapses it to render
// "Rejected"). The backend no longer derives or normalizes label text.
func TestSendControlResponse_ClaudePermissionBareDenyRetainsNativeResponse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"` + agent.ControlRejectedByUserMessage + `"}}}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, string(response), string(cr.Response),
		"the backend retains the native deny response verbatim; the frontend collapses the placeholder")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestSendControlResponse_ClaudePermissionDenyWithReasonRetainsMessage is the counterpart: a REAL
// typed reason survives verbatim into the native response for the frontend to render as feedback.
func TestSendControlResponse_ClaudePermissionDenyWithReasonRetainsMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	response := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"use ripgrep instead"}}}`)
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: response,
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, string(response), string(cr.Response), "a genuine typed rejection reason survives verbatim into the native response")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponse_UsesNestedRequestIDWhenTopLevelIDAlsoExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
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
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "req-1", cr.RequestID, "the nested control-response request_id keys the row, not the top-level JSON-RPC id")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

func TestSendControlResponseRejectsUnknownRequests(t *testing.T) {
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			svc, dispatcher, writer := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider,
			}))
			sends := 0
			svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
			dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: "agent-1",
				Content: []byte(`{"response":{"request_id":"missing","response":{"behavior":"allow"}}}`),
			}, writer)
			require.Empty(t, writer.errors)
			response := decodeQueueResponse(t, writer, &leapmuxv1.SendControlResponseResponse{})
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, response.State)
			require.NotEmpty(t, response.Error)
			require.Zero(t, sends)
			rows, err := svc.Queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}

// TestSendControlResponse_WithholdsDuplicateAnswerRow pins the idempotency claim: the SAME control
// request answered twice -- an RPC retry, or a second window answering before it received the cancel
// broadcast -- persists exactly ONE structured row + one scroll-rail dot, never two. The first
// answer claims the request id and does all the work; the duplicate (whose request row was deleted by
// the first) is a deduped no-op -- it draws no second row and is NOT re-forwarded to the agent.
func TestSendControlResponse_WithholdsDuplicateAnswerRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1", ClaimToken: "", Payload: []byte(`{"request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	answer := &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}
	dispatch(d, "SendControlResponse", answer, w)
	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors, "neither answer errors -- the winner forwards, the duplicate is a deduped no-op")

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1, "answering the same request twice draws exactly one row, not two")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestSendControlResponse_DuplicateAnswerDeletesRequestOnce pins the delete-gating half of the
// idempotency claim: firstAnswer controls deleteControlRequest, so ONLY the claim winner deletes
// the pending request and broadcasts its cancel. A duplicate answer (an RPC retry / a second window)
// therefore draws exactly ONE ControlCancel and one row -- never a second delete. Gating the delete
// on the winner is also what keeps the winner's request read while-present, so a concurrent duplicate
// can't tear the request out from under it and force a context-less / double-marked row.
func TestSendControlResponse_DuplicateAnswerDeletesRequestOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	registerAgentWatch(svc, "test-ch", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	answer := &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}
	dispatch(d, "SendControlResponse", answer, w)
	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors, "neither answer errors -- the winner forwards, the duplicate is a deduped no-op")

	cancels := 0
	for _, stream := range w.streamsSnapshot() {
		if _, ok := decodeWatchAgentEvent(t, stream).GetEvent().(*leapmuxv1.AgentEvent_ControlCancel); ok {
			cancels++
		}
	}
	assert.Equal(t, 1, cancels, "only the claim winner deletes the request, so the duplicate re-broadcasts no second cancel")

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the duplicate draws no second row")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestSendControlResponse_DuplicateDoesNotForward pins winner-only forwarding: a duplicate answer
// (RPC retry / second window) must NOT forward its response to the agent. A request-gone duplicate's
// resolution diverges from the winner's -- here a Codex plan-mode PROMPT approval, which the winner
// handles entirely server-side and never forwards, but whose duplicate resolves PlanModeControl==None
// (request-gone) and, before the fix, forwarded the stray approval envelope onto the agent's stdin.
// The forward is observed via a STOPPED agent: a duplicate that ATTEMPTED to forward would hit
// SendRawInput -> "agent not running" and surface an error; winner-only forward draws none.
func TestSendControlResponse_DuplicateDoesNotForward(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       marshalOptions(map[string]string{contracts.CodexOptionCollaborationMode: codex.CollaborationDefault}),
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "plan-session"}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentSessionID: "plan-session",
		AgentID:        "agent-1", RequestID: "plan-1",
		Payload: []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		ResumeSessionID: "plan-session",
		AgentID:         "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)

	// A plan-mode prompt approval with the DEFAULT clearContext=false. The winner handles it
	// server-side (never forwarded) and deletes the request, so the duplicate below reads request-gone
	// A duplicate must not forward this LeapMux-handled response.
	answer := &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`),
	}
	confirmPlanCollaborationForTest(t, svc)

	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors)
	_ = waitForMessageCount(t, svc, "agent-1", 2)

	// Stop the agent so any forward the duplicate ATTEMPTS surfaces as an "agent not running" error.
	svc.Agents.StopAgent("agent-1")
	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors,
		"a request-gone duplicate must not forward its diverged response -- winner-only forward draws no error even to a stopped agent")

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2,
		"the duplicate is a no-op: only the winner's one structured row + 'Implement the plan.' prompt, undoubled")
}

func TestSendControlResponseRejectsUnattributableContent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"foo":"bar"}`),
	}, w)
	require.Len(t, w.errors, 1)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rows, "an unattributable answer (no request id) persists no row")
}

// TestSendControlResponse_SkipsStructuredRowForSelfDisplayingTool asserts NO structured row is
// persisted for a self-displaying tool (ExitPlanMode) that is NOT clearing context -- its own
// ingested tool_result carries the mark, so a second synthetic row would double the rail dot.
func TestSendControlResponse_SkipsStructuredRowForSelfDisplayingTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
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
	require.Empty(t, rows, "a self-displaying tool's answer lives on its ingested tool_result, not a synthetic row")
}

func TestSendControlResponse_RestoresClaudeSelfDisplayedToolUseType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-ask",
		Payload: []byte(`{"type":"control_request","request_id":"req-ask","request":{"tool_name":"AskUserQuestion","tool_use_id":"toolu-ask"}}`),
	})
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	sink.ResetSpans()
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, sink, claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1",
		Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-ask","response":{"behavior":"allow","message":"Yes, continue."}}}`),
	}, w)
	require.Empty(t, w.errors)

	assert.Equal(t, claude.ToolNameAskUserQuestion, sink.GetSpanType("toolu-ask"),
		"the later Claude tool_result relies on this mapping to carry the CONTROL_RESPONSE mark")
}

func TestSendControlResponse_ClaudeExitPlanModeClearContextMarksStructuredRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	planPath := filepath.Join(t.TempDir(), "plan.md")
	writeTestPlanFile(t, planPath, "# Plan\n\nKeep the approval in the transcript.\n")
	require.NoError(t, svc.Queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
		ID: "agent-1", PlanFilePath: planPath, PlanTitle: "Plan",
	}))
	_, err := svc.InputQueue.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode","tool_use_id":"toolu-exit"}}`),
	})
	_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId:      "agent-1",
		PlanApproval: &leapmuxv1.PlanApprovalSettings{ClearContext: true},
		Content:      []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)

	// A context-clearing plan exit wipes Claude's own tool_result, so the structured row carries the
	// mark instead. Filter to CONTROL_RESPONSE rows to isolate it from any async plan-execution rows.
	controlRows := controlResponseRows(rows)
	require.Len(t, controlRows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, controlRows[0].Source)
	cr := decodeStructuredControlResponse(t, controlRows[0])
	assert.Equal(t, "CLAUDE_CODE", cr.Provider)
	assert.JSONEq(t, `{"type":"control_request","request_id":"req-1","request":{"tool_name":"ExitPlanMode","tool_use_id":"toolu-exit"}}`, string(cr.Request))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, controlRows[0].MarkType,
		"clear-context skips Claude's self-displayed tool_result, so the structured row must carry the rail dot")
}

// TestSendControlResponse_EnterPlanModeAllowTrimsRequestID pins the "trim everywhere" rule for
// the plan-mode transition: a control response whose request_id carries surrounding whitespace
// must still switch the agent to plan mode.
func TestSendControlResponse_EnterPlanModeAllowTrimsRequestID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"EnterPlanMode","tool_use_id":"toolu-enter"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
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
	got := loadOptions(testRegistry, dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, contracts.ClaudeModePlan, got[agent.OptionIDPermissionMode],
		"a whitespace-padded request_id must still apply the EnterPlanMode transition")
}

// TestApplyControlResponsePlanModeMutations pins the once-only plan-mode side effects the WINNER
// applies (the handler calls this inside `if firstAnswer`, so a duplicate never reaches it): an
// approved EnterPlanMode switches the agent into plan mode, while a request-gone answer -- which
// cannot resolve the transition -- touches nothing.
func TestApplyControlResponsePlanModeMutations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1",
		Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"EnterPlanMode","tool_use_id":"toolu-enter"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	content := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	plan := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, content)
	require.True(t, plan.requestMeta.Loaded)
	require.Equal(t, agent.PlanModeControlEnter, plan.resolution.PlanModeControl)

	before := loadOptions(testRegistry, dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[agent.OptionIDPermissionMode]
	require.NotEqual(t, contracts.ClaudeModePlan, before, "sanity: the agent is not already in plan mode")

	require.NoError(t, svc.applyControlResponsePlanModeMutations(dbAgent, plan))
	dbAgent, err = svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, contracts.ClaudeModePlan, loadOptions(testRegistry, dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[agent.OptionIDPermissionMode],
		"an approved EnterPlanMode applies the plan-mode switch")

	// A request-gone answer (no stored request to resolve the transition) mutates nothing.
	svc.Output.ClearPendingControlRequests("agent-1")
	gonePlan := buildControlResponsePlanForTest(t, svc, "agent-1", dbAgent, content)
	require.False(t, gonePlan.requestMeta.Loaded, "sanity: the request is gone")
	// Reset to Default so a no-op is distinguishable from a re-applied Enter transition (which sets Plan).
	dbAgent = svc.setAgentPermissionModeWithAgent(dbAgent, contracts.ClaudeModeDefault)
	require.NoError(t, svc.applyControlResponsePlanModeMutations(dbAgent, gonePlan))
	dbAgent, err = svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, contracts.ClaudeModeDefault, loadOptions(testRegistry, dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[agent.OptionIDPermissionMode],
		"a request-gone answer resolves no transition, so it mutates nothing")
}

// TestSendControlResponse_DuplicateStraddlingRestartStillDeduped is the #258 regression guard for
// the clear-context path: the approved answer's OWN restart (e.g. a clear-context ExitPlanMode) runs
// ClearAgentRuntimeState, which tears down every in-memory tracker -- but the idempotency claim is a
// DURABLE row, keyed by (agent, request, claim_token), so it survives that teardown. A DUPLICATE of the
// answer (an RPC retry, or a second window) echoing the SAME claim_token and arriving AFTER the restart
// is still deduped, drawing exactly ONE row, not two. Because the claim is durable rather than
// in-memory, this also holds across a full worker-PROCESS restart. Contrast
// TestSendControlResponse_ReusedRequestIDAfterRelaunchPersists, where the new subprocess re-issues the
// id with a FRESH claim_token, so a genuine post-relaunch answer persists.
func TestSendControlResponse_DuplicateStraddlingRestartStillDeduped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1", ClaimToken: "instA", Payload: []byte(`{"request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX), claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// Both answers echo the SAME instance token (a genuine duplicate -- an RPC retry / a second window
	// on the same instance), so the claim dedups on (agent, req, token).
	answer := &leapmuxv1.SendControlResponseRequest{
		AgentId:    "agent-1",
		Content:    []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`),
		ClaimToken: "instA",
	}
	dispatch(d, "SendControlResponse", answer, w)
	// The full restart the clear-context plan exit triggers (initiatePlanExecutionRestart ->
	// ClearAgentRuntimeState = ClearPendingControlRequests + CleanupAgent). Neither touches the durable
	// answer claim, so the claim survives and the duplicate below (same token) is deduped.
	svc.Output.ClearAgentRuntimeState("agent-1")
	dispatch(d, "SendControlResponse", answer, w)
	require.Empty(t, w.errors, "neither answer errors -- the winner forwards, the straddling duplicate is a deduped no-op")

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a duplicate straddling the restart is deduped -- the surviving claim draws exactly one row")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestSendControlResponse_ReusedRequestIDAfterRelaunchPersists is the counterpart: after a relaunch,
// the new subprocess re-issues a request that REUSES the id (its JSON-RPC counter restarted from
// scratch). PublishControlRequest mints a FRESH claim_token for that new instance, which the frontend
// echoes back, so the genuine post-relaunch answer claims a distinct key and persists its OWN row --
// while a stale duplicate of the PRE-relaunch instance (old token) is still deduped, so the reuse
// window stays closed. The distinct claim_token -- not a release step -- is what tells the two apart.
func TestSendControlResponse_ReusedRequestIDAfterRelaunchPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1", ClaimToken: "instA", Payload: []byte(`{"request":{"tool_name":"Bash"}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, sink, claudetest.StartEcho)
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	content := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`)
	// Instance A answers with its echoed token.
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: content, ClaimToken: "instA"}, w)

	// Relaunch, then the new subprocess re-issues a request that reuses id "req-1" -- PublishControlRequest
	// mints a fresh token, which the frontend would echo. Read it back (as the frontend does via the
	// broadcast) and answer with it: this genuine post-relaunch answer claims a distinct key and persists.
	svc.Output.ClearPendingControlRequests("agent-1")
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "req-1", Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`)}))
	reissued, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
	require.NoError(t, err)
	require.NotEqual(t, "instA", reissued.ClaimToken, "the reissued instance minted a distinct token")
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: content, ClaimToken: reissued.ClaimToken}, w)

	// A stale duplicate of the PRE-relaunch instance (old token) is still deduped -- the reuse window is closed.
	dispatch(d, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: content, ClaimToken: "instA"}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2, "the two distinct instances persist their own rows; instance A's stale duplicate draws none")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[1].MarkType)
}

// TestSendControlResponse_CursorCreatePlanPersistsOnlyStructuredRow covers the Cursor createPlan
// path, where the provider REWRITES resolution.Content into an ACP outcome. The answer must route
// to exactly ONE structured row (marked CONTROL_RESPONSE) whose response is the transformed outcome
// forwarded to the agent -- never two rows.
func TestSendControlResponse_CursorCreatePlanPersistsOnlyStructuredRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "7",
		Payload: []byte(`{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`),
	})
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), claudetest.StartEcho)
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
	require.Len(t, rows, 1, "exactly the structured answer row -- no double-render")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "CURSOR", cr.Provider)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`, string(cr.Request))
	// The response is the TRANSFORMED ACP outcome that was forwarded to Cursor, not the raw envelope.
	var outcome struct {
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(cr.Response, &outcome))
	assert.Equal(t, "rejected", outcome.Result.Outcome.Outcome)
	assert.Equal(t, "Needs tests.", outcome.Result.Outcome.Reason)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType,
		"a control-response answer draws a scroll-rail jump dot")
}

// TestSendControlResponse_CursorCreatePlanApprovePersistsOnlyStructuredRow is the ALLOW sibling:
// createPlan is PlanModeControlNone, so the Allow branch must be a NO-OP (no permission-mode
// switch) while the answer still routes to the single structured "accepted" row.
func TestSendControlResponse_CursorCreatePlanApprovePersistsOnlyStructuredRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}))
	createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "plan-7",
		Payload: []byte(`{"jsonrpc":"2.0","id":"plan-7","method":"cursor/create_plan","params":{}}`),
	})
	before, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", Options: map[string]string{agent.OptionIDModel: "opus"}, WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR), claudetest.StartEcho)
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
	require.Len(t, rows, 1, "exactly the structured answer row -- no double-render")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	cr := decodeStructuredControlResponse(t, rows[0])
	var outcome struct {
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(cr.Response, &outcome))
	assert.Equal(t, "accepted", outcome.Result.Outcome.Outcome)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)

	// createPlan is PlanModeControlNone, so the Allow branch must not touch permission mode.
	after, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before.Options, after.Options, "approving createPlan must not change agent options/permission mode")
}

// TestNeedsStructuredRow pins the single "does this answer get a synthetic structured row" rule.
// A resolvable request id is required. A genuinely self-displayed answer draws no row except when a
// context-clearing plan exit wipes its echoed tool_result. Otherwise the answer gets the row -- for
// EVERY provider, and even when the stored request is gone (#258): there is no provider-capability
// guard here. #258 survives for an answer CLAIMED while its request existed --
// ListControlResponsesAwaitingRecording recovers that delivered answer after a restart deleted the
// request row. An answer that ARRIVES with no request row never reaches this decision, because
// SendControlResponse refuses it. The "answered twice" duplicate (whose first answer's tool_result
// already carries the mark) is deduped one layer up by SendControlResponse's idempotency claim, so it
// never reaches this decision a second time -- the withhold guard the old SelfDisplaysControlAnswers
// capability provided is subsumed by that claim and intentionally gone.
func TestNeedsStructuredRow(t *testing.T) {
	t.Parallel()

	// mk sets only the fields needsTranscriptRow reads: the resolvable request id and the resolved
	// self-display flag. requestMeta.Loaded no longer participates -- the "answered twice" duplicate
	// is deduped one layer up by SendControlResponse's idempotency claim, not here.
	mk := func(requestID string, selfDisplayed bool) controlResponsePlan {
		var plan controlResponsePlan
		plan.requestMeta.RequestID = requestID
		plan.resolution.SelfDisplayed = selfDisplayed
		return plan
	}
	// selfDisplayedExit builds a genuinely self-displayed plan for the exitPlanClearingContext branch.
	selfDisplayedExit := func(clear bool) controlResponsePlan {
		plan := mk("req-1", true)
		plan.decision.Response.Response.Behavior = agent.ControlBehaviorAllow
		plan.resolution.PlanModeControl = agent.PlanModeControlExit
		plan.settings = &leapmuxv1.PlanApprovalSettings{ClearContext: clear}
		return plan
	}

	assert.False(t, mk("", false).needsTranscriptRow(),
		"no request id -> the answer is unattributable and draws no row")

	// Not self-displayed: the answer gets the row for EVERY provider -- Claude permission answers
	// included, and even when the stored request is gone (#258: an answer claimed while its request
	// existed, which the replay sweep recovers after the request row disappeared). No
	// provider-capability guard here: a request-gone duplicate whose first answer already carries
	// the mark is stopped earlier by the idempotency claim, so it never reaches this decision twice.
	assert.True(t, mk("req-1", false).needsTranscriptRow(),
		"not self-displayed -> structured row")

	// A genuinely self-displayed answer: its own tool_result owns the mark, except a context-clearing
	// plan exit wipes that row, where the structured row carries it instead.
	assert.False(t, selfDisplayedExit(false).needsTranscriptRow(),
		"self-displayed, not clearing context -> tool_result owns the mark, no structured row")
	assert.True(t, selfDisplayedExit(true).needsTranscriptRow(),
		"self-displayed, context-clearing plan exit wipes the tool_result -> structured row carries the mark")
}

func TestExitPlanClearingContext(t *testing.T) {
	t.Parallel()

	// The single triple that needsTranscriptRow keys the mark-carrying row off (and that controls the
	// once-only initiatePlanExecution): an APPROVED ExitPlanMode that ALSO clears context (the
	// transcript row is wiped, so the structured row carries the mark and the approval is not
	// forwarded). The stored request must identify a plan exit.
	mk := func(behavior string, ctrl agent.PlanModeControlKind, clear bool) controlResponsePlan {
		var plan controlResponsePlan
		plan.decision.Response.Response.Behavior = behavior
		plan.settings = &leapmuxv1.PlanApprovalSettings{ClearContext: clear}
		plan.resolution.PlanModeControl = ctrl
		return plan
	}
	assert.True(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlExit, true).exitPlanClearingContext())
	assert.False(t, mk(agent.ControlBehaviorDeny, agent.PlanModeControlExit, true).exitPlanClearingContext(), "deny is not an approval")
	assert.False(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlEnter, true).exitPlanClearingContext(), "enter is not exit")
	assert.False(t, mk(agent.ControlBehaviorAllow, agent.PlanModeControlExit, false).exitPlanClearingContext(), "not clearing context")
}

func TestResolveTargetMode(t *testing.T) {
	t.Parallel()

	// The frontend's attached permission mode wins when present.
	assert.Equal(t, contracts.ClaudeModePlan, resolveTargetMode(contracts.ClaudeModePlan, contracts.ClaudeModeDefault))
	// Empty falls back to the caller's default -- the ONE thing the plan-prompt path
	// (contracts.ClaudeModeDefault) and the ExitPlanMode path (contracts.ClaudeModeAcceptEdits) differ on.
	assert.Equal(t, contracts.ClaudeModeDefault, resolveTargetMode("", contracts.ClaudeModeDefault))
	assert.Equal(t, contracts.ClaudeModeAcceptEdits, resolveTargetMode("", contracts.ClaudeModeAcceptEdits))
}

// An empty response must remain empty and retain its transcript row.
func TestPersistControlResponseRow_EmptyContentPersistsRowNotDropped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	var plan controlResponsePlan
	plan.requestMeta.RequestID = "req-1"
	plan.resolution.Content = []byte{}

	persistControlResponseForTest(t, svc, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, plan)

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1, "an empty-but-non-nil Content must not silently drop the answer row")
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "req-1", cr.RequestID)
	assert.Empty(t, cr.Response)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// Malformed response bytes must remain available in the transcript and Raw JSON view.
func TestPersistControlResponseRow_InvalidJSONContentPersistsRowNotDropped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	var plan controlResponsePlan
	plan.requestMeta.RequestID = "req-1"
	plan.resolution.Content = []byte("not json at all")

	persistControlResponseForTest(t, svc, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, plan)

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a non-empty but invalid-JSON Content must not silently drop the answer row")
	cr := decodeStructuredControlResponse(t, rows[0])
	assert.Equal(t, "req-1", cr.RequestID)
	assert.Equal(t, "not json at all", string(cr.Response))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
}

// TestPersistControlResponseRow_IsUserSourcedCountsAsUserMessage pins that the structured
// control-response row is USER-sourced (not LEAPMUX). A control answer is the user's own response to
// the agent, so -- matching how every non-Claude provider-resolved answer was persisted on the
// pre-#258 path (a USER {content} row) -- it counts toward HasUserMessages (the resume-decision
// predicate in resolveResumeSessionID: answering a control request alone can make a session
// resumable) and its bubble exposes data-role="user". The isSynthetic flag, NOT the source, routes it
// to the control_response renderer, so USER source does not make it render as a plain user bubble. The
// row seq (1) is above the default session_start_seq (0), so HasUserMessages returning true is due to
// the USER source, not the seq gate.
func TestPersistControlResponseRow_IsUserSourcedCountsAsUserMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	var plan controlResponsePlan
	plan.requestMeta.RequestID = "req-1"
	plan.resolution.Content = []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	persistControlResponseForTest(t, svc, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, plan)

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source,
		"a control-response answer is the user's own response, persisted as a USER row")
	assert.Greater(t, rows[0].Seq, int64(0), "sanity: the row is above the default session_start_seq, so only the source decides HasUserMessages")

	hasUser, err := svc.Queries.HasUserMessages(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, hasUser,
		"a control-response answer counts as a user message for resolveResumeSessionID -- answering alone can make a session resumable")
}

// TestPersistControlResponseRow_MakesSessionResumable pins the USER-source decision at the actual
// resume DECISION point (resolveResumeSessionID), not just its HasUserMessages building block: a
// non-resumed agent whose ONLY interaction is answering a control request -- no typed prompt -- goes
// from not-resumable to resumable once the answer row is persisted, because that row is now a USER
// message. This is the auto-started / control-answer-only session the pre-uniform-USER LEAPMUX source
// excluded from resume; uniform USER includes it. The seq of the answer row is above the
// session_start_seq recorded at UpdateAgentSessionID, so the flip is due to the source, not the seq gate.
func TestPersistControlResponseRow_MakesSessionResumable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	// Agent startup assigns a session ID (and records session_start_seq). resumed==0, so resumability
	// hinges entirely on whether a USER message exists past session_start_seq.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-A", ID: "agent-1",
	}))

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	require.Empty(t, svc.resolveResumeSessionID("agent-1", dbAgent.AgentSessionID, dbAgent.Resumed),
		"before any answer, a never-typed-into session is not resumable")

	var plan controlResponsePlan
	plan.requestMeta.RequestID = "req-1"
	plan.resolution.Content = []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	persistControlResponseForTest(t, svc, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, plan)

	dbAgent, err = svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "session-A",
		svc.resolveResumeSessionID("agent-1", dbAgent.AgentSessionID, dbAgent.Resumed),
		"answering a control request alone makes the session resumable -- the answer is a USER row")
}

// TestProcessControlResponse pins the dispatcher-free forward decision the RPC handler keys off: the
// extracted processControlResponse reports the bytes to forward for a plain answer, and forward=false
// for a deduped duplicate and a server-side plan-mode prompt. Exercising the tuple directly (no
// channel sender) is exactly the testability seam the extraction bought.
func TestProcessControlResponse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("forwards a plain permission answer, then dedups its duplicate", func(t *testing.T) {
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "req-1", ClaimToken: "tok-1",
			Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
		})
		dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
		require.NoError(t, err)
		content := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`)

		bytes, forward, responseErr := processControlResponseForTest(svc, "agent-1", dbAgent, content, "tok-1")
		require.NoError(t, responseErr)
		require.True(t, forward, "a plain permission answer is forwarded to the agent")
		assert.Equal(t, content, bytes, "the forwarded bytes are the (unrewritten) response content")

		// The duplicate echoes the SAME instance token, so it lost the idempotency claim taken by the
		// first call and forwards nothing.
		bytes, forward, responseErr = processControlResponseForTest(svc, "agent-1", dbAgent, content, "tok-1")
		require.NoError(t, responseErr)
		assert.False(t, forward, "a duplicate answer (same claim token) is a deduped no-op and is not re-forwarded")
		assert.Nil(t, bytes)
	})

	t.Run("a reissued instance's answer (fresh claim token) forwards despite the reused request_id", func(t *testing.T) {
		// The id-reuse closure at the processControlResponse level: a stale duplicate of a prior instance
		// (old token) is deduped, but the genuine answer to a reissued request whose new instance minted a
		// FRESH token is forwarded, not withheld as a duplicate of the reused id.
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "req-1", ClaimToken: "instA",
			Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
		})
		dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
		require.NoError(t, err)
		content := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`)

		_, forward, responseErr := processControlResponseForTest(svc, "agent-1", dbAgent, content, "instA")
		require.NoError(t, responseErr)
		require.True(t, forward, "instance A's answer forwards")
		// Re-store req-1 (the reissued instance) and answer with a DIFFERENT token.
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "req-1", ClaimToken: "instB",
			Payload: []byte(`{"type":"control_request","request_id":"req-1","request":{"tool_name":"Bash"}}`),
		})
		_, forward, responseErr = processControlResponseForTest(svc, "agent-1", dbAgent, content, "instB")
		require.NoError(t, responseErr)
		assert.True(t, forward, "the reissued instance's answer (fresh token) forwards -- not withheld as a duplicate of the reused id")

		// A stale duplicate of instance A (old token) is STILL deduped.
		_, forward, responseErr = processControlResponseForTest(svc, "agent-1", dbAgent, content, "instA")
		require.NoError(t, responseErr)
		assert.False(t, forward, "instance A's stale duplicate stays deduped even after instance B answered")
	})

	t.Run("does not forward a plan-mode prompt (handled server-side)", func(t *testing.T) {
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			Options:       marshalOptions(map[string]string{contracts.CodexOptionCollaborationMode: codex.CollaborationDefault}),
		}))
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "plan-1",
			Payload: []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		})
		dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
		require.NoError(t, err)

		bytes, forward, responseErr := processControlResponseForTest(svc, "agent-1", dbAgent,
			[]byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`), "plan-tok")
		require.NoError(t, responseErr)
		assert.False(t, forward, "a plan-mode prompt is handled server-side, never forwarded")
		assert.Nil(t, bytes)
	})

	// A provider that cannot build a frame its agent can parse sets resolution.Withhold.
	// An empty Content cannot carry that meaning: resolveControlResponsePlan backfills the
	// raw frontend bytes over it, so clearing Content forwards the exact envelope the
	// provider refused to send -- a frame the app-server cannot parse, on its stdin.
	t.Run("a provider's withheld resolution is not forwarded", func(t *testing.T) {
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))
		// The stored request has no native wire ID, so the provider must refuse delivery.
		createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "req-gone", ClaimToken: "tok-1", Payload: []byte(`{"request":{"tool_name":"Bash"}}`),
		})
		dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
		require.NoError(t, err)
		content := []byte(`{"type":"control_response","response":{"request_id":"req-gone","response":{"behavior":"allow"}}}`)

		bytes, forward, responseErr := processControlResponseForTest(svc, "agent-1", dbAgent, content, "tok-1")
		require.ErrorContains(t, responseErr, "could not read")
		assert.False(t, forward, "no frame may reach the app-server's stdin")
		assert.Nil(t, bytes)
	})

	// The plan-exit fallback mode is the PROVIDER's own word. Shared code that stamped
	// Claude's `acceptEdits` persisted a mode ZCode's session/setMode rejects, so the
	// mode chip showed a value the session had never entered.
	t.Run("an approved plan exit lands on the provider's own mode", func(t *testing.T) {
		cases := []struct {
			name     string
			provider leapmuxv1.AgentProvider
			toolName string
			want     string
		}{
			{
				"claude", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				claude.ToolNameExitPlanMode, contracts.ClaudeModeAcceptEdits,
			},
			{
				"zcode", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				contracts.ZCodeToolNameExitPlanMode, contracts.ZCodeModeBuild,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				svc, _, _ := setupTestService(t)
				require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
					ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
					AgentProvider: tc.provider,
				}))
				payload := `{"type":"control_request","request_id":"plan-1","id":7,` +
					`"method":"interaction/requestUserInput",` +
					`"request":{"tool_name":"` + tc.toolName + `","tool_use_id":"call-1"}}`
				createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "plan-1", Payload: []byte(payload), ClaimToken: "plan-tok",
				})
				dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
				require.NoError(t, err)

				// Approve with NO permissionMode: the banner's unchecked state, which is
				// what makes the provider's own fallback the answer.
				_, _, responseErr := processControlResponseForTest(svc, "agent-1", dbAgent,
					[]byte(`{"type":"control_response","response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`),
					"plan-tok")
				require.NoError(t, responseErr)

				updated, err := svc.Queries.GetAgentByID(ctx, "agent-1")
				require.NoError(t, err)
				opts := map[string]string{}
				require.NoError(t, json.Unmarshal([]byte(updated.Options), &opts))
				assert.Equal(t, tc.want, opts[agent.OptionIDPermissionMode])
			})
		}
	})
}

func TestProcessControlResponseKeepsInvalidAnswersRetryable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		request  string
		invalid  string
		valid    string
	}{
		{
			name:     "mcp elicitation",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			request:  `{"id":"req-1","method":"mcpServer/elicitation/request","params":{"message":"Choose a value","requestedSchema":{"type":"object"}}}`,
			invalid:  `{"response":{"request_id":"req-1","response":{"action":"invalid"}}}`,
			valid:    `{"response":{"request_id":"req-1","response":{"action":"decline"}}}`,
		},
		{
			name:     "pi mcp approval",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
			request:  `{"type":"extension_ui_request","id":"req-1","method":"select","title":"MCP: form_probe_echo\n\nArguments:\n{}","options":["Allow once","Allow for session","Deny"]}`,
			invalid:  `{"response":{"request_id":"req-1","response":{"action":"accept","_meta":{"persist":"always"}}}}`,
			valid:    `{"response":{"request_id":"req-1","response":{"action":"decline"}}}`,
		},
		{
			name:     "zcode permission",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			request:  `{"type":"control_request","request_id":"req-1","id":7,"method":"interaction/requestUserInput","request":{"tool_name":"Bash","tool_use_id":"call-1"}}`,
			invalid:  `{"response":{"request_id":"req-1","response":{"behavior":"invalid"}}}`,
			valid:    `{"response":{"request_id":"req-1","response":{"behavior":"deny"}}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			svc, dispatcher, writer := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: test.provider,
			}))
			createTestControlRequest(t, ctx, svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "req-1", ClaimToken: "claim-1", Payload: []byte(test.request),
			})
			row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
			require.NoError(t, err)
			dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: "agent-1", Content: []byte(test.invalid), ClaimToken: "claim-1",
			}, writer)
			require.Empty(t, writer.errors)
			response := decodeQueueResponse(t, writer, &leapmuxv1.SendControlResponseResponse{})
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.State)
			require.NotEmpty(t, response.Error)
			pending, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: "agent-1", RequestID: "req-1"})
			require.NoError(t, err, "the provider still waits for a valid response")
			assert.Equal(t, "claim-1", pending.ClaimToken)
			_, forward, responseErr := processControlResponseForTest(svc, "agent-1", row, []byte(test.valid), "claim-1")
			require.NoError(t, responseErr)
			assert.True(t, forward, "the invalid answer must not consume the response claim")
		})
	}
}

func buildControlResponsePlanForTest(t *testing.T, svc *Service, agentID string, row db.Agent, content []byte) controlResponsePlan {
	t.Helper()
	plugin := testRegistry.Plugin(row.AgentProvider)
	meta, err := svc.loadControlResponseRequestMetadata(agentID, plugin.ControlResponseRequestID(content))
	require.NoError(t, err)
	return resolveControlResponsePlan(plugin, meta, content, nil)
}

// confirmPlanCollaborationForTest acknowledges only the expected Codex plan-mode change.
// The process fixture supplies input delivery; this callback supplies the separate settings protocol.
func confirmPlanCollaborationForTest(t *testing.T, svc *Service) {
	t.Helper()
	svc.updateAgentSettingsFn = func(agentID string, options OptionMap) agent.SettingsApplyResult {
		assert.Equal(t, "agent-1", agentID)
		assert.Equal(t, OptionMap{contracts.CodexOptionCollaborationMode: codex.CollaborationDefault}, options)
		value := codex.CollaborationDefault
		return agent.SettingsApplyResult{
			AppliedLive:     true,
			Settlements:     agent.OptionSettlements{contracts.CodexOptionCollaborationMode: {State: agent.OptionSettlementConfirmed, Value: &value}},
			SurfacedOptions: OptionMap{contracts.CodexOptionCollaborationMode: value},
		}
	}
}
