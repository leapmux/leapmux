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
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func decodeWatchAgentMessage(t *testing.T, stream *leapmuxv1.InnerStreamMessage) *leapmuxv1.AgentChatMessage {
	t.Helper()

	var resp leapmuxv1.WatchEventsResponse
	require.NoError(t, proto.Unmarshal(stream.GetPayload(), &resp))

	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)

	msg := agentEvent.GetAgentMessage()
	require.NotNil(t, msg)
	return msg
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

func TestSendAgentMessage_SlashClearBroadcastsUserBeforeContextCleared(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		// Leave AgentProvider unspecified so restart fails immediately and
		// deterministically without spawning a real agent process.
	}))

	sender := channel.NewSender(w)
	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    sender,
	})

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "/clear",
	}, w)

	require.Empty(t, w.errors)
	require.NotEmpty(t, w.streams)

	var roles []leapmuxv1.MessageRole
	var types [][]string
	for _, stream := range w.streams {
		msg := decodeWatchAgentMessage(t, stream)
		roles = append(roles, msg.Role)
		types = append(types, decodeMessageTypes(t, msg))
	}

	userIdx := -1
	contextClearedIdx := -1
	for i := range roles {
		if userIdx == -1 && roles[i] == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
			userIdx = i
		}
		if contextClearedIdx == -1 {
			for _, typ := range types[i] {
				if typ == "context_cleared" {
					contextClearedIdx = i
					break
				}
			}
		}
	}

	require.NotEqual(t, -1, userIdx, "expected a streamed user message")
	require.NotEqual(t, -1, contextClearedIdx, "expected a streamed context_cleared notification")
	assert.Less(t, userIdx, contextClearedIdx, "the /clear user message must be streamed before context_cleared")
}

func TestSendAgentRawMessage_CodexInterruptPersistsSyntheticUserMarker(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-codex",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	sender := channel.NewSender(w)
	svc.Watchers.WatchAgent("agent-codex", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    sender,
	})

	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-codex",
		Content: `{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}`,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.streams, 1)

	msg := decodeWatchAgentMessage(t, w.streams[0])
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, msg.Role)
	assert.Equal(t, "[Request interrupted by user]", decodeAgentChatMessageContent(t, msg)["content"])
}

func TestSendAgentRawMessage_ClaudeInterruptDoesNotPersistSyntheticUserMarker(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-claude",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	sender := channel.NewSender(w)
	svc.Watchers.WatchAgent("agent-claude", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    sender,
	})

	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-claude",
		Content: `{"type":"control_request","request":{"subtype":"interrupt"}}`,
	}, w)

	require.Empty(t, w.errors)
	assert.Empty(t, w.streams)
}
