package deepseekharness

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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

// A child session reports every input as a native `user/message`. LeapMux stores the
// message of each input that its own client sent, so only an input that nobody at
// LeapMux stored is a transcript row: the prompt that the parent agent gave the child.
// Source: `@deepseek-ai/dsh` 0.2.0-rc.2, probed on a live child. A client prompt carries
// `rpcId` (dsh-api-session-controller prompt, dsh-subagent), and the parent prompt does not.
// The native process also injects the instruction file and the runtime context as user messages.
func TestChildUserMessagesStoreOnlyThePromptThatLeapMuxDidNotStore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		source string
		stored bool
	}{
		{"the prompt of the parent agent", `{"kind":"user"}`, true},
		{"a prompt that a client sent", `{"kind":"user","rpcId":"request-1"}`, false},
		{"the instruction file", `{"kind":"agent-instructions","form":"instructions","baseline":true}`, false},
		{"the runtime context", `{"kind":"runtime-context","form":"snapshot","sections":[]}`, false},
		{"a settled subagent notice", `{"kind":"subagent-settled","form":"notice"}`, false},
		{"a message with no source", ``, false},
		{"a message with an unreadable source", `"user"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			stream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
			source := ""
			if tc.source != "" {
				source = `,"source":` + tc.source
			}
			raw := []byte(`{"type":"user/message","seq":8,"time":1000,"data":{"content":[{"type":"text","text":"The exact native text."}],"role":"user","id":"native-message"` + source + `}}`)
			require.NoError(t, a.handleSessionEvent(stream, raw))
			messages := sink.Child("stored-child").Messages()
			if !tc.stored {
				assert.Empty(t, messages)
				assert.Equal(t, int64(8), stream.lastSeq, "a native event that LeapMux does not store still advances the cursor")
				return
			}
			require.Len(t, messages, 1)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[0].Source)
			assert.Equal(t, raw, messages[0].Content, "the stored row keeps the original native bytes")
		})
	}
}

func TestRootUserMessageIsNeverStoredFromTheNativeSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	raw := []byte(`{"type":"user/message","seq":8,"time":1000,"data":{"content":[{"type":"text","text":"The exact native text."}],"source":{"kind":"user"},"role":"user","id":"native-message"}}`)
	require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
	assert.Empty(t, sink.Messages())
}

// The native process states the context window of a model in `request/context`, and it states
// the window again only when the provider, the model, or the window changes. The usage of an
// assistant message carries no window, so the Worker adds the window of the stream to the
// supplement of each assistant block, and the browser reads it from there.
// Source: `@deepseek-ai/dsh` 0.2.0-rc.2, dsh-agent-loop (the emit condition of request/context).
func TestAssistantMessageCarriesTheContextWindowOfItsStream(t *testing.T) {
	t.Parallel()
	requestContext := func(window string) string {
		return `{"type":"request/context","seq":SEQ,"time":1000,"data":{"provider":"deepseek-official","model":"deepseek-flash","contextWindow":` + window + `}}`
	}
	assistant := `{"type":"assistant/message","seq":SEQ,"time":1001,"data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"Thought."},{"type":"text","text":"Answer."}]},"usage":{"inputTokens":0,"outputTokens":0}}}`
	var seq int
	feed := func(t *testing.T, a *Agent, stream *sessionStream, frame string) {
		t.Helper()
		seq++
		require.NoError(t, a.handleSessionEvent(stream, []byte(strings.Replace(frame, "SEQ", strconv.Itoa(seq), 1))))
	}
	supplements := func(sink *agenttest.Sink) []string {
		var result []string
		for _, message := range sink.Messages() {
			if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && strings.Contains(string(message.Content), `"assistant/message"`) {
				result = append(result, string(message.SupplementalContent))
			}
		}
		return result
	}
	t.Run("each block states the window that the last request context stated", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		feed(t, a, a.streams["root"], requestContext("1000000"))
		feed(t, a, a.streams["root"], assistant)
		feed(t, a, a.streams["root"], requestContext("128000"))
		feed(t, a, a.streams["root"], assistant)
		assert.JSONEq(t, `{"blockIndex":0,"contextWindow":1000000}`, supplements(sink)[0])
		assert.JSONEq(t, `{"blockIndex":1,"contextWindow":1000000}`, supplements(sink)[1])
		assert.JSONEq(t, `{"blockIndex":0,"contextWindow":128000}`, supplements(sink)[2])
		assert.JSONEq(t, `{"blockIndex":1,"contextWindow":128000}`, supplements(sink)[3])
	})
	t.Run("a message before any request context states no window", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		feed(t, a, a.streams["root"], assistant)
		assert.JSONEq(t, `{"blockIndex":0}`, supplements(sink)[0])
	})
	t.Run("an invalid window keeps the last valid one", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		feed(t, a, a.streams["root"], requestContext("1000000"))
		for _, invalid := range []string{"0", "-1", "1.5", `"1000000"`, "null"} {
			feed(t, a, a.streams["root"], requestContext(invalid))
		}
		feed(t, a, a.streams["root"], assistant)
		assert.JSONEq(t, `{"blockIndex":0,"contextWindow":1000000}`, supplements(sink)[0])
	})
	t.Run("a child stream keeps its own window", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		child := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
		feed(t, a, a.streams["root"], requestContext("1000000"))
		feed(t, a, child, requestContext("64000"))
		feed(t, a, child, assistant)
		childMessages := sink.Child("stored-child").Messages()
		var childSupplements []string
		for _, message := range childMessages {
			if strings.Contains(string(message.Content), `"assistant/message"`) {
				childSupplements = append(childSupplements, string(message.SupplementalContent))
			}
		}
		require.Len(t, childSupplements, 2)
		assert.JSONEq(t, `{"blockIndex":0,"contextWindow":64000}`, childSupplements[0])
	})
	t.Run("a resumed session takes the window from its snapshot projection", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		snapshot := `{"type":"item","streamId":"root","value":{"type":"snapshot","cursor":-1,"records":[],"hasMore":false,"projections":{"values":{"contextPressure":{"pressureTokens":1,"projectedTokens":16,"contextWindow":1000000}}}}}`
		require.NoError(t, a.handleFrame([]byte(snapshot)))
		feed(t, a, a.streams["root"], assistant)
		assert.JSONEq(t, `{"blockIndex":0,"contextWindow":1000000}`, supplements(sink)[0])
	})
	t.Run("an empty projection states no window", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		snapshot := `{"type":"item","streamId":"root","value":{"type":"snapshot","cursor":-1,"records":[],"hasMore":false,"projections":{"values":{"contextPressure":{}}}}}`
		require.NoError(t, a.handleFrame([]byte(snapshot)))
		feed(t, a, a.streams["root"], assistant)
		assert.JSONEq(t, `{"blockIndex":0}`, supplements(sink)[0])
	})
}
