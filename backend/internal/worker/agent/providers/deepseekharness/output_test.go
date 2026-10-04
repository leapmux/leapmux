package deepseekharness

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingAssistantBlockSink struct {
	*agenttest.Sink
	calls  int
	failAt int
	cause  error
}

func TestNativeTurnEndCountsResetAndReachTheProviderBeforeIdle(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	provider := Registration().Plugin
	var seq int64
	appendEvent := func(kind string, data any) []byte {
		raw, err := json.Marshal(map[string]any{"type": kind, "seq": seq, "time": 1790955900000 + seq, "data": data})
		require.NoError(t, err)
		require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
		seq++
		return raw
	}
	for index, count := range []int{0, 2, 0} {
		turn := index + 1
		appendEvent("turn/start", map[string]any{"turn": turn})
		for call := range count {
			id := "native-count-call-" + strconv.Itoa(call)
			appendEvent("tool/call", map[string]any{"turn": turn, "callId": id, "name": "bash", "arguments": `{}`})
			appendEvent("tool/result", map[string]any{"turn": turn, "message": map[string]any{"toolCallId": id, "isError": false, "content": []any{map[string]any{"type": "text", "text": "The native command completed."}}}})
		}
		end := appendEvent("turn/end", map[string]any{"turn": turn, "reason": map[string]any{"kind": "completed"}})
		rows := sink.Messages()
		last := rows[len(rows)-1]
		assert.True(t, last.TurnEnd)
		assert.Equal(t, end, last.Content)
		content := agent.MessageContent{Original: last.Content, Metadata: last.Metadata}
		actual, present := provider.TurnEndToolUses(agent.ResolveMessageContent(provider, content))
		require.True(t, present)
		assert.Equal(t, int32(count), actual)
	}
	assert.Equal(t, []int{0, 2, 0}, agenttest.TurnToolUseCounts(t, sink.Messages()))
	assert.Equal(t, []string{
		"turn_active:true", "turn_end", "turn_active:false",
		"turn_active:true", "turn_end", "turn_active:false",
		"turn_active:true", "turn_end", "turn_active:false",
	}, sink.TurnLifecycle())
}

func (s *failingAssistantBlockSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.calls++
	if s.calls == s.failAt {
		return s.cause
	}
	return s.Sink.PersistMessage(source, content, span)
}

func TestFailedAssistantBlockDoesNotAdvanceTheNativeCursorOrDuplicateItsPriorBlock(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	cause := errors.New("the second native block write failed")
	sink := &failingAssistantBlockSink{Sink: base, failAt: 2, cause: cause}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	stream := a.streams["root"]
	raw := []byte(`{"type":"assistant/message","seq":17,"time":1000,"data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"The first exact block."},{"type":"text","text":"The second exact block."}]}}}`)
	require.ErrorIs(t, a.handleSessionEvent(stream, raw), cause)
	assert.Equal(t, int64(-1), stream.lastSeq, "the failed native event remains available for replay")
	require.Len(t, base.Messages(), 1)
	require.NoError(t, a.handleSessionEvent(stream, raw))
	messages := base.Messages()
	require.Len(t, messages, 2, "the successful first block must not duplicate when the native event replays")
	assert.JSONEq(t, `{"blockIndex":0}`, string(messages[0].SupplementalContent))
	assert.JSONEq(t, `{"blockIndex":1}`, string(messages[1].SupplementalContent))
	assert.Equal(t, int64(17), stream.lastSeq)
}

func TestChildReplayKeepsOneCopyOfEachExactNativeAssistantBlock(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	raw := []byte(`{"type":"assistant/message","seq":17,"time":1000,"data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"The first exact child block."},{"type":"text","text":"The second exact child block."}]}}}`)
	for range 2 {
		stream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
		require.NoError(t, a.handleSessionEvent(stream, raw))
	}
	child := sink.Child("stored-child")
	messages := child.Messages()
	require.Len(t, messages, 2, "a fresh follow stream must reuse immutable native record identities")
	assert.JSONEq(t, `{"blockIndex":0}`, string(messages[0].SupplementalContent))
	assert.JSONEq(t, `{"blockIndex":1}`, string(messages[1].SupplementalContent))
}

func TestNativeCancellationClosesTheTurnAsInterrupted(t *testing.T) {
	t.Parallel()
	for _, cause := range []string{"user", "parent", "disposed", "legacy"} {
		t.Run(cause, func(t *testing.T) {
			base := &agenttest.Sink{}
			a := newOfflineAgent(t, base)
			a.active = true
			raw := []byte(`{"type":"turn/end","seq":17,"time":1000,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"` + cause + `"}}}}`)
			require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
			messages := base.Messages()
			require.Len(t, messages, 1)
			assert.Equal(t, agent.MessageCompletionInterrupted, messages[0].Completion)
			assert.False(t, a.active)
		})
	}
}
