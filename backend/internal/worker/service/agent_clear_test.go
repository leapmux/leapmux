package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// decodeWatchAgentEvent returns the AgentEvent payload from a stream message
// without requiring it to be an AgentMessage. Use this when the test cares
// about a mix of AgentMessage and StatusChange broadcasts.
func decodeWatchAgentEvent(t *testing.T, stream *leapmuxv1.InnerStreamMessage) *leapmuxv1.AgentEvent {
	t.Helper()

	var resp leapmuxv1.WatchEventsResponse
	require.NoError(t, proto.Unmarshal(stream.GetPayload(), &resp))

	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	return agentEvent
}

func decodeMessageTypes(t *testing.T, msg *leapmuxv1.AgentChatMessage) []string {
	t.Helper()

	raw, err := msgcodec.Decompress(msg.Content, msg.ContentCompression)
	require.NoError(t, err)

	var top map[string]any
	require.NoError(t, json.Unmarshal(raw, &top))

	if messages, ok := top["messages"].([]any); ok && len(messages) > 0 {
		types := make([]string, 0, len(messages))
		for _, entry := range messages {
			obj, ok := entry.(map[string]any)
			require.True(t, ok)
			typ, _ := obj["type"].(string)
			if typ != "" {
				types = append(types, typ)
			}
		}
		return types
	}

	typ, _ := top["type"].(string)
	if typ == "" {
		return nil
	}
	return []string{typ}
}

func decodeAgentChatMessageContent(t *testing.T, msg *leapmuxv1.AgentChatMessage) map[string]any {
	t.Helper()

	raw, err := msgcodec.Decompress(msg.Content, msg.ContentCompression)
	require.NoError(t, err)

	var top map[string]any
	require.NoError(t, json.Unmarshal(raw, &top))
	return top
}

func TestSendAgentRawMessage_CodexInterruptPersistsSyntheticUserMarker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-codex",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	registerAgentWatch(svc, w.channelID, "agent-codex", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-codex",
		Content: `{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}`,
	}, w)

	require.Empty(t, w.errors)
	var msg *leapmuxv1.AgentChatMessage
	for _, stream := range w.streamsSnapshot() {
		if candidate := decodeWatchAgentEvent(t, stream).GetAgentMessage(); candidate != nil {
			msg = candidate
			break
		}
	}
	require.NotNil(t, msg)
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msg.Source)
	assert.Equal(t, "[Request interrupted by user]", decodeAgentChatMessageContent(t, msg)["content"])
	snapshot, err := svc.InputQueue.Snapshot(ctx, "agent-codex")
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snapshot.PauseReason)
}

func TestIsInterruptRequestRecognizesProviderFormats(t *testing.T) {
	t.Parallel()

	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	cursor := leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR

	assert.True(t, agent.IsInterruptRequest(pi, `{"type":"abort"}`), "Pi abort RPC should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(codex, `{"jsonrpc":"2.0","method":"turn/interrupt"}`), "Codex turn interrupt should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(claude, `{"type":"control_request","request":{"subtype":"interrupt"}}`), "Claude control interrupt should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(cursor, `{"jsonrpc":"2.0","method":"session/cancel"}`), "ACP session/cancel should be treated as an interrupt")

	// Each classifier only matches its own format — cross-provider payloads
	// must not be misclassified.
	assert.False(t, agent.IsInterruptRequest(claude, `{"type":"abort"}`))
	assert.False(t, agent.IsInterruptRequest(pi, `{"jsonrpc":"2.0","method":"turn/interrupt"}`))

	assert.False(t, agent.IsInterruptRequest(pi, `{"type":"prompt","message":"abort"}`))
	assert.False(t, agent.IsInterruptRequest(codex, `not json`))
}

func TestSendAgentRawMessage_ClaudeInterruptDoesNotPersistSyntheticUserMarker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-claude",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	registerAgentWatch(svc, w.channelID, "agent-claude", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-claude",
		Content: `{"type":"control_request","request":{"subtype":"interrupt"}}`,
	}, w)

	require.Empty(t, w.errors)
	for _, stream := range w.streamsSnapshot() {
		assert.Nil(t, decodeWatchAgentEvent(t, stream).GetAgentMessage())
	}
}
