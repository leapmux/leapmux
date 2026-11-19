package kimi

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationNoticePreservesNativeFieldsWithoutEndingTheTurn(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"info", "warning", "error"} {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"current","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			raw, err := json.Marshal(map[string]any{"type": "notice", "level": level, "code": "HOOK_BLOCKED", "title": "Hook UserPromptSubmit blocked", "detail": "Native reason 🙂"})
			require.NoError(t, err)
			rig.conversationFrame(t, string(raw))
			awaitKimiTestOutput(t, rig.agent.sink)
			require.Equal(t, 1, rig.sink.NotificationCount())
			message := rig.sink.LastNotification()
			view, err := decodeKimiConversationRecord(message.Content)
			require.NoError(t, err)
			require.NotNil(t, view.notice)
			assert.Equal(t, level, view.notice.Level)
			assert.Equal(t, "Hook UserPromptSubmit blocked", *view.notice.Title)
			assert.Equal(t, "Native reason 🙂", view.notice.Detail)
			require.NotNil(t, view.notice.Code)
			assert.Equal(t, "HOOK_BLOCKED", *view.notice.Code)
			assert.Equal(t, "session_1", message.AgentSessionID)
			assert.Empty(t, message.SpanID)
			assert.False(t, message.TurnEnd)
			assert.True(t, rig.agent.PublishTurnActive().Active)
			rig.conversationFrame(t, string(raw))
			awaitKimiTestOutput(t, rig.agent.sink)
			assert.Equal(t, 2, rig.sink.NotificationCount(), "the native feed gives identical deliveries no shared event identity")
		})
	}
}

type kimiNoticeRetrySink struct {
	agent.ProviderServices
	refuse atomic.Bool
	mu     sync.Mutex
	keys   []string
}

func (sink *kimiNoticeRetrySink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	sink.mu.Lock()
	sink.keys = append(sink.keys, content.IdempotencyKey)
	sink.mu.Unlock()
	written, err := sink.ProviderServices.PersistNotification(source, content)
	if err == nil && sink.refuse.Load() {
		return written, errors.New("notice acknowledgement unavailable after storage")
	}
	return written, err
}

func TestKimiConversationNoticeRetriesKeepOneReceiptPerDelivery(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	queue := agent.OutputQueueFor(rig.agent.sink)
	direct, valid := agent.DirectTranscript(rig.agent.sink).(agent.ProviderServices)
	require.True(t, valid)
	retry := &kimiNoticeRetrySink{ProviderServices: direct}
	retry.refuse.Store(true)
	rig.agent.sink = agent.NewOrderedProviderServices(queue, retry)
	notice := `{"type":"notice","level":"warning","title":"Native warning"}`
	rig.conversationFrame(t, notice)
	require.True(t, queue.HasPending())
	assert.Equal(t, 1, rig.sink.NotificationCount())
	retry.refuse.Store(false)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.False(t, queue.HasPending())
	assert.Equal(t, 1, rig.sink.NotificationCount(), "retrying an admitted write cannot repeat its native delivery")
	retry.mu.Lock()
	keys := append([]string(nil), retry.keys...)
	retry.mu.Unlock()
	require.NotEmpty(t, keys)
	require.NotEmpty(t, keys[0])
	for _, key := range keys {
		assert.Equal(t, keys[0], key)
	}
	rig.conversationFrame(t, notice)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 2, rig.sink.NotificationCount(), "a second native delivery keeps a new Worker receipt")
}

func TestKimiConversationNoticeKeepsItsExactAgentAndSession(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	notice := `{"type":"notice","level":"warning","title":"Child warning","detail":"Native child detail"}`
	rig.agent.HandleOutput(kimiConversationTestFrame(t, "foreign", "agent-0", notice))
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "unknown-child", notice))
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Zero(t, rig.sink.NotificationCount())
	assert.Zero(t, rig.sink.Child(child.childAgentID).NotificationCount())
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", notice))
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Zero(t, rig.sink.NotificationCount())
	output := rig.sink.Child(child.childAgentID)
	require.Equal(t, 1, output.NotificationCount())
	view, err := decodeKimiConversationRecord(output.LastNotification().Content)
	require.NoError(t, err)
	assert.Equal(t, "agent-0", view.record.AgentID)
	assert.Equal(t, "session_1", view.record.SessionID)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiConversationNoticeRemainsVisibleWhileHistoryIsPending(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.awaitConversationSnapshot(rig.agent.run(kimiMainAgentID))
	rig.conversationFrame(t, `{"type":"notice","level":"error","title":"Native warning during history recovery","code":"HISTORY_PENDING"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 1, rig.sink.NotificationCount())
	assert.NotNil(t, rig.agent.run(kimiMainAgentID).conversationRecovery.Load())
	assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(), "a diagnostic does not admit input before the native page")
}
