package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type turnEndCapturingWriter struct {
	channelID string
	mu        sync.Mutex
	last      *leapmuxv1.AgentTurnEnd
}

func (m *turnEndCapturingWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error {
	return nil
}
func (m *turnEndCapturingWriter) SendError(_ int32, _ string) error { return nil }
func (m *turnEndCapturingWriter) SendStream(s *leapmuxv1.InnerStreamMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := &leapmuxv1.WatchEventsResponse{}
	if err := proto.Unmarshal(s.GetPayload(), resp); err != nil {
		return nil
	}
	if te := resp.GetAgentEvent().GetTurnEnd(); te != nil {
		m.last = te
	}
	return nil
}
func (m *turnEndCapturingWriter) ChannelID() string   { return m.channelID }
func (*turnEndCapturingWriter) MaxPayloadBudget() int { return 0 }
func (*turnEndCapturingWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

func (m *turnEndCapturingWriter) lastTurnEnd() *leapmuxv1.AgentTurnEnd {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func newTurnEndFixture(t *testing.T) (*agentOutputSink, *turnEndCapturingWriter) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-turn-end",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	mock := &turnEndCapturingWriter{channelID: "ch-turn-end"}
	registerAgentWatch(svc, "ch-turn-end", "agent-turn-end", leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, mock)

	newTestRootOutputSink(t, svc.Output, "agent-turn-end", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	sink := requireRootOutputSink(t, svc.Output, "agent-turn-end")
	return sink, mock
}

// The turn end broadcasts its event, and the turn's tool count waits for the
// activity settle, which carries it to the client (AgentActivityChanged).
func TestPersistTurnEnd_BroadcastsTurnEndAndRecordsToolUses(t *testing.T) {
	t.Parallel()

	sink, mock := newTurnEndFixture(t)

	require.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"result","num_tool_uses":4}`)}, agent.SpanInfo{}))

	require.NotNil(t, mock.lastTurnEnd())
	count := unspentToolCount(sink.h, "agent-turn-end")
	require.NotNil(t, count, "the settle that follows spends the count the turn end recorded")
	assert.Equal(t, int32(4), *count)
}

func TestPersistTurnEnd_BroadcastsTurnEndWithoutToolUsesWhenAbsent(t *testing.T) {
	t.Parallel()

	sink, mock := newTurnEndFixture(t)

	require.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"result","subtype":"success"}`)}, agent.SpanInfo{}))

	require.NotNil(t, mock.lastTurnEnd())
	assert.Nil(t, unspentToolCount(sink.h, "agent-turn-end"),
		"a missing num_tool_uses leaves the count unknown, not 0")
}
