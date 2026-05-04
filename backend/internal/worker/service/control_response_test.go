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
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
		Model:      "opus",
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
}

func TestSendControlResponse_PersistsCodexFeedbackMessage(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
		Model:      "opus",
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
}

func TestSendControlResponse_BroadcastsCancelBeforeSyntheticMessage(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
		Model:      "opus",
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
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
		Model:      "opus",
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
}

func TestSendControlResponse_PersistsGeminiPermissionSelectionLabel(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
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
		Model:      "auto",
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI))
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
}

func TestExtractControlResponseRequestID(t *testing.T) {
	// Claude Code format: response.request_id
	assert.Equal(t, "req-1", extractControlResponseRequestID(
		[]byte(`{"response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
	))

	// OpenCode / ACP JSON-RPC format: numeric id
	assert.Equal(t, "5", extractControlResponseRequestID(
		[]byte(`{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"once"}}}`),
	))

	// OpenCode / ACP JSON-RPC format: string id
	assert.Equal(t, "abc-123", extractControlResponseRequestID(
		[]byte(`{"jsonrpc":"2.0","id":"abc-123","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`),
	))

	// No request ID
	assert.Equal(t, "", extractControlResponseRequestID(
		[]byte(`{"type":"unknown"}`),
	))

	// Null id
	assert.Equal(t, "", extractControlResponseRequestID(
		[]byte(`{"id":null}`),
	))

	// Invalid JSON
	assert.Equal(t, "", extractControlResponseRequestID(
		[]byte(`not json`),
	))
}
