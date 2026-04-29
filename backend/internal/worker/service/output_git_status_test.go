package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// agentEventCapturingWriter captures the most recent AgentStatusChange so
// individual fields can be asserted in BroadcastGitStatus tests.
type agentEventCapturingWriter struct {
	channelID string
	mu        sync.Mutex
	last      *leapmuxv1.AgentStatusChange
	streamCnt int64
}

func (m *agentEventCapturingWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error {
	return nil
}
func (m *agentEventCapturingWriter) SendError(_ int32, _ string) error { return nil }
func (m *agentEventCapturingWriter) SendStream(s *leapmuxv1.InnerStreamMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamCnt++
	resp := &leapmuxv1.WatchEventsResponse{}
	if err := proto.Unmarshal(s.GetPayload(), resp); err != nil {
		return nil
	}
	if sc := resp.GetAgentEvent().GetStatusChange(); sc != nil {
		m.last = sc
	}
	return nil
}
func (m *agentEventCapturingWriter) ChannelID() string { return m.channelID }

func (m *agentEventCapturingWriter) lastStatus() *leapmuxv1.AgentStatusChange {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func (m *agentEventCapturingWriter) count() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streamCnt
}

// newGitStatusFixture mints a minimal service + sink against a tempdir agent.
// The tempdir is *not* a git repo, so gitutil.GetGitStatus returns an empty
// AgentGitStatus — sufficient to assert event shape without dragging git
// state into the test. Returns the concrete *agentOutputSink so tests can
// reach BroadcastGitStatus directly (it's not on the agent.OutputSink
// interface — providers don't call it; the sink fires it automatically
// at turn-end).
func newGitStatusFixture(t *testing.T) (*agentOutputSink, *agentEventCapturingWriter) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Model:         "opus",
		Effort:        "high",
	}))

	mock := &agentEventCapturingWriter{channelID: "ch-1"}
	w := &EventWatcher{ChannelID: "ch-1", Sender: channel.NewSender(mock)}
	svc.Watchers.WatchAgent("agent-1", w)

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE).(*agentOutputSink)
	return sink, mock
}

// TestBroadcastGitStatus_EmitsPartialStatusChange verifies that
// BroadcastGitStatus produces exactly one AgentStatusChange with
// Status=UNSPECIFIED (so the frontend treats other fields as
// "no change") and only AgentId + GitStatus populated. This is the
// contract that lets per-turn callers refresh git state without
// re-shipping the full settings/catalog payload.
func TestBroadcastGitStatus_EmitsPartialStatusChange(t *testing.T) {
	sink, mock := newGitStatusFixture(t)

	sink.BroadcastGitStatus()

	assert.Equal(t, int64(1), mock.count(), "BroadcastGitStatus should fan out exactly one event")
	sc := mock.lastStatus()
	require.NotNil(t, sc, "captured event must be a StatusChange")

	assert.Equal(t, "agent-1", sc.GetAgentId())
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, sc.GetStatus(),
		"BroadcastGitStatus must NOT set Status — UNSPECIFIED tells the frontend to leave other fields as-is")
	// Catalog/settings fields must remain unset so the frontend doesn't
	// overwrite its last-known catalog with empties.
	assert.Empty(t, sc.GetModel(), "Model must not be repopulated on a git-status refresh")
	assert.Empty(t, sc.GetEffort(), "Effort must not be repopulated on a git-status refresh")
	assert.Empty(t, sc.GetPermissionMode(), "PermissionMode must not be repopulated on a git-status refresh")
	assert.Empty(t, sc.GetAgentSessionId(), "AgentSessionId must not be repopulated on a git-status refresh")
	assert.False(t, sc.GetWorkerOnline(), "WorkerOnline must remain default-false; statusChange handler treats UNSPECIFIED status as a partial update")
	assert.Empty(t, sc.GetAvailableModels(), "AvailableModels must not be re-shipped")
	assert.Empty(t, sc.GetAvailableOptionGroups(), "AvailableOptionGroups must not be re-shipped")
	assert.Empty(t, sc.GetExtraSettings(), "ExtraSettings must not be re-shipped")
}

// TestBroadcastGitStatus_RepeatedCallsBroadcastEachTime confirms the call
// is not throttled or memoized at the sink layer — every per-turn
// invocation produces an event so the frontend always sees the latest
// gitStatus snapshot.
func TestBroadcastGitStatus_RepeatedCallsBroadcastEachTime(t *testing.T) {
	sink, mock := newGitStatusFixture(t)

	for i := 0; i < 3; i++ {
		sink.BroadcastGitStatus()
	}

	assert.Equal(t, int64(3), mock.count(),
		"BroadcastGitStatus must broadcast each call; debouncing belongs higher in the stack if needed")
}

// TestPersistMessage_TurnEnd_AutoBroadcastsGitStatus locks in the
// turn-end contract: every provider's TURN_END-role persist (Codex
// turn/completed, Claude type:"result", ACP prompt response, Pi
// agent_end) auto-fires BroadcastGitStatus so providers don't have to.
// The auto-fire runs on a goroutine to keep the agent's stdout-read
// loop free of the git-subprocess + DB latency, so the test polls.
func TestPersistMessage_TurnEnd_AutoBroadcastsGitStatus(t *testing.T) {
	sink, mock := newGitStatusFixture(t)

	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageRole_MESSAGE_ROLE_TURN_END,
		[]byte(`{"type":"result","subtype":"success"}`),
		agent.SpanInfo{},
	))

	require.Eventually(t, func() bool {
		return mock.lastStatus() != nil
	}, time.Second, 10*time.Millisecond,
		"TURN_END persist should auto-fire BroadcastGitStatus on a goroutine")
	sc := mock.lastStatus()
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, sc.GetStatus(),
		"auto-fired git-status broadcast must use the partial-update shape")
}

// TestPersistMessage_NonTurnEnd_DoesNotBroadcastGitStatus confirms the
// auto-fire is gated on TURN_END only — assistant/system/user persists
// must not pay the git-status cost on every message.
func TestPersistMessage_NonTurnEnd_DoesNotBroadcastGitStatus(t *testing.T) {
	sink, mock := newGitStatusFixture(t)

	for _, role := range []leapmuxv1.MessageRole{
		leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM,
	} {
		require.NoError(t, sink.PersistMessage(role, []byte(`{}`), agent.SpanInfo{}))
	}

	// Give any spurious goroutine fire enough time to land. With the
	// gate working correctly, no goroutine ever starts.
	time.Sleep(50 * time.Millisecond)
	sc := mock.lastStatus()
	assert.Nil(t, sc, "non-TURN_END persists must not auto-broadcast git status")
}
