package codex

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexNoticeFailingSink struct {
	*agenttest.Sink
	mu       sync.Mutex
	failures int
	attempts int
	child    *codexNoticeFailingSink
}

func TestCodexPendingNoticeCountsItsIdentityBytes(t *testing.T) {
	t.Parallel()
	a := newCodexAgentWithSink(agentapi.NewProviderServices(&agenttest.Sink{}))
	event := codexPendingChildEvent{kind: codexPendingNotification, raw: []byte(`{}`), noticeKey: strings.Repeat("x", codexPendingChildEventBytesLimit)}
	a.enqueuePendingCodexChildEvent("child-thread", event)
	a.Mu.Lock()
	state := a.collabChildren["child-thread"]
	assert.Zero(t, len(state.pendingEvents))
	assert.Zero(t, state.pendingEventBytes)
	a.Mu.Unlock()
}

func (s *codexNoticeFailingSink) PersistNotification(source leapmuxv1.MessageSource, content agentapi.MessageContent) (bool, error) {
	s.mu.Lock()
	s.attempts++
	fail := s.failures > 0
	if fail {
		s.failures--
	}
	s.mu.Unlock()
	if fail {
		return false, errors.New("the notice write failed")
	}
	return s.Sink.PersistNotification(source, content)
}

func (s *codexNoticeFailingSink) ChildSink(id string) agentapi.ProviderServices {
	if s.child != nil {
		return agentapi.NewProviderServices(s.child)
	}
	return s.Sink.ChildSink(id)
}

const codexNoticeChildStarted = `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/child"}}}`

func TestCodexNoticeRetriesAWriteThatDidNotReachStorage(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"main", "child", "queued child"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			root := &codexNoticeFailingSink{Sink: &agenttest.Sink{}}
			target := root
			threadID := "main-thread"
			if route != "main" {
				threadID = "child-thread"
				root.child = &codexNoticeFailingSink{Sink: root.Child("child-of-spawn")}
				target = root.child
			}
			target.failures = 1
			a := newCodexAgentWithSink(agentapi.NewProviderServices(root))
			a.mainTurn.id = "root-turn"
			if route == "child" {
				feedCodexLines(a, codexNoticeChildStarted)
			}
			notice := fmt.Sprintf(`{"method":"configWarning","params":{"threadId":%q,"summary":"retryable warning"}}`, threadID)
			feedCodexLines(a, notice)
			if route == "queued child" {
				feedCodexLines(a, notice)
				require.Zero(t, target.attempts, "the notice waits for its route")
				feedCodexLines(a, codexNoticeChildStarted)
			}
			require.Equal(t, 1, target.attempts)
			require.Zero(t, target.NotificationCount())
			feedCodexLines(a, notice, notice)
			assert.Equal(t, 2, target.attempts, "retry the failed write, then deduplicate the stored notice")
			assert.Equal(t, 1, target.NotificationCount())
			if target != root {
				assert.Zero(t, root.NotificationCount())
			}
		})
	}
}

func TestCodexNoticeRejectedByThePendingQueueCanArriveAgain(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.mainTurn.id = "root-turn"
	for range codexPendingChildEventLimit {
		feedCodexLines(a, `{"method":"warning","params":{"threadId":"child-thread","message":"queued"}}`)
	}
	notice := `{"method":"configWarning","params":{"threadId":"child-thread","summary":"retryable warning"}}`
	feedCodexLines(a, notice, codexNoticeChildStarted, notice, notice)
	count := 0
	for _, message := range sink.Child("child-of-spawn").PersistedNotifications() {
		if string(message.Content) == notice {
			count++
		}
	}
	assert.Equal(t, 1, count, "a rejected enqueue is not a stored notice")
}
