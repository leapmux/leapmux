package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestSendAgentMessage_AutoStartBroadcastsStartingDuringEnsureRunning verifies
// that when SendAgentMessage triggers ensureAgentRunning on an INACTIVE agent
// (e.g. after a worker/desktop restart that killed the subprocess), the
// auto-start path broadcasts a STARTING AgentStatusChange. Without this, the
// chat startup banner stays hidden during the restart window even though a
// just-typed message is queued behind the still-cold subprocess — the bubble
// pulses but no progress affordance is shown beneath it.
func TestSendAgentMessage_AutoStartBroadcastsStartingDuringEnsureRunning(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	// Mock a successful auto-start so the happy path is exercised without
	// spawning a real subprocess.
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
		Content: "hello",
	}, w)

	require.Empty(t, w.errors)

	sawStarting := false
	var startingMessage string
	for _, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		sc := ev.GetStatusChange()
		if sc == nil {
			continue
		}
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING {
			sawStarting = true
			startingMessage = sc.GetStartupMessage()
			break
		}
	}

	assert.True(t, sawStarting,
		"expected STARTING status change while ensureAgentRunning auto-starts the cold subprocess, so the chat startup banner can render beneath the queued user message")
	assert.NotEmpty(t, startingMessage,
		"the STARTING broadcast must carry a phase label so the banner renders something readable (e.g. \"Starting Claude Code…\")")
}

// TestSendAgentMessage_AutoStartFailureRevertsToInactive verifies that when
// ensureAgentRunning's startAgent call fails, the broadcast sequence ends in
// INACTIVE — not STARTING (which would leave the banner spinning forever) and
// not STARTUP_FAILED (which would mark the agent permanently unusable; the
// existing design intentionally keeps it retryable on the next send by
// surfacing the failure as a per-message delivery_error instead).
func TestSendAgentMessage_AutoStartFailureRevertsToInactive(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, assert.AnError
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
		Content: "hello",
	}, w)

	require.Empty(t, w.errors)

	startingIdx := -1
	inactiveIdx := -1
	startupFailedIdx := -1
	for i, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		sc := ev.GetStatusChange()
		if sc == nil {
			continue
		}
		switch sc.GetStatus() {
		case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
			if startingIdx == -1 {
				startingIdx = i
			}
		case leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE:
			if inactiveIdx == -1 {
				inactiveIdx = i
			}
		case leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED:
			if startupFailedIdx == -1 {
				startupFailedIdx = i
			}
		}
	}

	require.NotEqual(t, -1, startingIdx, "expected a STARTING broadcast at the start of ensureAgentRunning")
	require.NotEqual(t, -1, inactiveIdx, "expected an INACTIVE broadcast after auto-start failure so the startup banner clears")
	assert.Less(t, startingIdx, inactiveIdx, "INACTIVE must follow STARTING so the spinner clears after the failed attempt")
	assert.Equal(t, -1, startupFailedIdx,
		"auto-start failure on the SendAgentMessage path must NOT mark the agent STARTUP_FAILED — the message gets a delivery_error and the agent stays retryable on the next send")
}
