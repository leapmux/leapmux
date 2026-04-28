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

func TestSendAgentMessage_SlashClearBroadcastsUserBeforeContextCleared(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	// Mock a successful restart so we can validate the happy-path ordering
	// without spawning a real agent process. context_cleared is only
	// broadcast when the new agent starts successfully.
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
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

	userIdx := -1
	contextClearedIdx := -1
	for i, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		if msg := ev.GetAgentMessage(); msg != nil {
			if userIdx == -1 && msg.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
				userIdx = i
			}
			if contextClearedIdx == -1 {
				for _, typ := range decodeMessageTypes(t, msg) {
					if typ == "context_cleared" {
						contextClearedIdx = i
						break
					}
				}
			}
		}
	}

	require.NotEqual(t, -1, userIdx, "expected a streamed user message")
	require.NotEqual(t, -1, contextClearedIdx, "expected a streamed context_cleared notification")
	assert.Less(t, userIdx, contextClearedIdx, "the /clear user message must be streamed before context_cleared")
}

// TestSendAgentMessage_SlashClearBroadcastsStartingDuringRestart verifies
// that handleClearContext broadcasts a STARTING AgentStatusChange between
// the user message and the context_cleared notification. Without this, a
// frontend whose agent.status was non-ACTIVE (e.g. INACTIVE after a worker
// restart that killed the agent process) sees nothing during the restart
// window — the thinking indicator stays hidden until context_cleared lands,
// at which point isAgentWorking flips it back off again, so the indicator
// never actually shows. The STARTING broadcast also gives the startup
// panel a phase label to display.
func TestSendAgentMessage_SlashClearBroadcastsStartingDuringRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	// Mock a successful restart so we exercise the happy path without
	// spawning a real agent process.
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return &leapmuxv1.AgentSettings{}, nil
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
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

	userIdx := -1
	startingIdx := -1
	activeIdx := -1
	contextClearedIdx := -1
	for i, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		if msg := ev.GetAgentMessage(); msg != nil {
			if userIdx == -1 && msg.Role == leapmuxv1.MessageRole_MESSAGE_ROLE_USER {
				userIdx = i
			}
			if contextClearedIdx == -1 {
				for _, typ := range decodeMessageTypes(t, msg) {
					if typ == "context_cleared" {
						contextClearedIdx = i
						break
					}
				}
			}
		}
		if sc := ev.GetStatusChange(); sc != nil {
			switch sc.GetStatus() {
			case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
				if startingIdx == -1 {
					startingIdx = i
				}
			case leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE:
				if activeIdx == -1 {
					activeIdx = i
				}
			}
		}
	}

	require.NotEqual(t, -1, userIdx, "expected a streamed user message")
	require.NotEqual(t, -1, startingIdx, "expected a STARTING status change to be broadcast during /clear restart")
	require.NotEqual(t, -1, contextClearedIdx, "expected a streamed context_cleared notification")
	require.NotEqual(t, -1, activeIdx, "expected an ACTIVE status change after /clear restart succeeds")
	assert.Less(t, userIdx, startingIdx, "STARTING must be broadcast after the /clear user message")
	assert.Less(t, startingIdx, contextClearedIdx, "STARTING must be broadcast before context_cleared so the frontend's ACTIVE-only indicator gate is satisfied during the restart window")
	// context_cleared must arrive before ACTIVE so the startup banner is
	// replaced atomically by the new notification message instead of
	// blanking out for the DB-write window between them.
	assert.Less(t, contextClearedIdx, activeIdx, "context_cleared must be persisted before ACTIVE so the startup banner is replaced atomically by the notification message")
}

func TestSendAgentMessage_SlashClearRestartFailureSkipsContextCleared(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		// Unspecified provider → StartAgent returns an error, exercising
		// the failure branch of handleClearContext.
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

	sawAgentError := false
	sawContextCleared := false
	sawStartupFailed := false
	for _, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		if msg := ev.GetAgentMessage(); msg != nil {
			for _, typ := range decodeMessageTypes(t, msg) {
				switch typ {
				case "agent_error":
					sawAgentError = true
				case "context_cleared":
					sawContextCleared = true
				}
			}
		}
		if sc := ev.GetStatusChange(); sc != nil {
			if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
				sawStartupFailed = true
			}
		}
	}

	assert.True(t, sawAgentError, "expected an agent_error notification when restart fails")
	assert.False(t, sawContextCleared, "context_cleared must not be broadcast when the restart fails — clients would otherwise think the agent is ready")
	assert.True(t, sawStartupFailed, "expected a STARTUP_FAILED status change so the frontend's startup panel shows the error instead of staying stuck on STARTING")
}

func TestSendAgentRawMessage_CodexInterruptPersistsSyntheticUserMarker(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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

func TestIsInterruptRequestRecognizesProviderFormats(t *testing.T) {
	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	gemini := leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI

	assert.True(t, agent.IsInterruptRequest(pi, `{"type":"abort"}`), "Pi abort RPC should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(codex, `{"jsonrpc":"2.0","method":"turn/interrupt"}`), "Codex turn interrupt should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(claude, `{"type":"control_request","request":{"subtype":"interrupt"}}`), "Claude control interrupt should be treated as an interrupt")
	assert.True(t, agent.IsInterruptRequest(gemini, `{"jsonrpc":"2.0","method":"session/cancel"}`), "ACP session/cancel should be treated as an interrupt")

	// Each classifier only matches its own format — cross-provider payloads
	// must not be misclassified.
	assert.False(t, agent.IsInterruptRequest(claude, `{"type":"abort"}`))
	assert.False(t, agent.IsInterruptRequest(pi, `{"jsonrpc":"2.0","method":"turn/interrupt"}`))

	assert.False(t, agent.IsInterruptRequest(pi, `{"type":"prompt","message":"abort"}`))
	assert.False(t, agent.IsInterruptRequest(codex, `not json`))
}

func TestSendAgentRawMessage_ClaudeInterruptDoesNotPersistSyntheticUserMarker(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

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
