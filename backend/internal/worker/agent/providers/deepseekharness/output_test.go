package deepseekharness

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownFramesPersist(t *testing.T) {
	stored := []byte(`{"type":"future/event","seq":1,"time":1000,"data":{"future":true}}`)
	frame := func(stream string) []byte {
		return []byte(`{"type":"item","streamId":"` + stream + `","value":{"type":"event","event":` + string(stored) + `}}`)
	}
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "root", Frame: frame("root"), Stored: stored},
		{Name: "other session", Frame: frame("foreign"), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		require.NoError(t, a.handleFrame(tc.Frame))
		return sink
	})
}

func TestInboxSpliceKeepsTheKnownSteerIdentity(t *testing.T) {
	for _, id := range []string{"", " ", "\t"} {
		t.Run(strconv.Quote(id), func(t *testing.T) {
			a := newOfflineAgent(t, &agenttest.Sink{})
			steer := &pendingSteer{requestID: "request", itemID: "known-item"}
			a.pendingSteers = []*pendingSteer{steer}
			raw, err := json.Marshal(map[string]any{
				"type": "agent/inbox/spliced", "seq": 1, "time": 1000,
				"data": map[string]any{"inserted": []any{map[string]any{"id": id, "source": map[string]string{"rpcId": "request"}}}},
			})
			require.NoError(t, err)
			require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
			assert.Equal(t, "known-item", steer.itemID, "a malformed update must not prevent native steer removal")
		})
	}
}

func TestSessionEventVerdicts(t *testing.T) {
	for _, kind := range []string{
		"agent-preset/selected", "approval/asked", "approval/decided", "approval/policy", "assistant/attempt", "command/done", "command/run",
		"compaction/prune", "compaction/summary", "developer/message", "feedback/message-delete", "feedback/message-put", "feedback/record", "hook/invoked",
		"image/offload", "job/registered", "job/settled", "llm/retry-started", "model/selection", "permission/preset", "request/header", "sandbox/mode", "schedule/change",
		"session-log-deepseek/delivery-accepted", "session/end-seed", "session/title", "session/title-llm-request", "step/start", "step/end",
		"subagent/descriptor", "subagent/model-selection-policy", "system/message", "team/member", "team/message/delivered", "team/message/queued", "team/task",
		"tool/ptc-dispatch", "tool/ptc-dispatch-start", "web/deepseek-search-llm-request", "workspace/changes", "request/context", "compaction/start", "agent/inbox/spliced",
	} {
		t.Run(kind, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			raw, err := json.Marshal(map[string]any{"type": kind, "seq": 1, "time": 1000, "data": map[string]any{}})
			require.NoError(t, err)
			require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
	for _, tc := range []struct {
		data   string
		stored bool
	}{
		{`{"decision":"pass","exitCode":0}`, false},
		{`{"decision":"pass","exitCode":1}`, true},
		{`{"decision":"deny","exitCode":0}`, true},
		{`{"decision":"block"}`, true},
		{`{"decision":"stop"}`, true},
		{`{"decision":"pass","stderrSummary":"cannot start the hook"}`, true},
		{`{"decision":"allow","exitCode":0,"stderrSummary":"an informational line"}`, false},
		{`{"decision":"pass"}`, false},
	} {
		t.Run(tc.data, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"hook/result","seq":1,"time":1000,"data":`+tc.data+`}`)))
			if tc.stored {
				assert.Len(t, sink.PersistedNotifications(), 1)
			} else {
				assert.Empty(t, sink.PersistedNotifications())
			}
			assert.Empty(t, sink.Messages())
		})
	}
	t.Run("deliverables", func(t *testing.T) {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		raw := []byte(`{"type":"deliverables/presented","seq":1,"time":1000,"data":{"files":[{"path":"report.md","description":"Report"}]}}`)
		require.NoError(t, a.handleSessionEvent(a.streams["root"], raw))
		require.Len(t, sink.PersistedNotifications(), 1)
		assert.JSONEq(t, string(raw), string(sink.PersistedNotifications()[0].Content))
	})
}

func TestLlmRetryResolvesAtRootOutput(t *testing.T) {
	for _, output := range []string{
		`{"type":"assistant/message","seq":6,"time":1000,"data":{"message":{"content":[{"type":"text","text":"answer"}]}}}`,
		`{"type":"tool/call","seq":6,"time":1000,"data":{"callId":"call","name":"read","arguments":"{}"}}`,
	} {
		t.Run(output, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			stream := a.streams["root"]
			for seq := range 5 {
				// .tmp/probe/deepseekharness/logs/frames.jsonl, retry frames 115-140.
				raw, err := json.Marshal(map[string]any{"type": "llm/retry", "seq": seq, "time": 1000, "data": map[string]any{"retryId": "retry", "retry": seq + 1, "maxRetries": 5, "failure": map[string]any{"message": "overloaded"}}})
				require.NoError(t, err)
				require.NoError(t, a.handleSessionEvent(stream, raw))
				stream.lastSeq = -1
				require.NoError(t, a.handleSessionEvent(stream, raw))
				assert.Equal(t, agent.NotificationKindAPIRetry, Registration().Plugin.Classify(raw).Kind())
			}
			require.Len(t, sink.PersistedNotifications(), 5)
			require.NoError(t, a.handleSessionEvent(stream, []byte(output)))
			assert.Equal(t, []map[string]interface{}{{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved}}, sink.LeapMuxNotifications())
		})
	}
}

func TestLlmRetryEndsWithTheRootTurn(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	stream := a.streams["root"]
	for _, raw := range []string{
		`{"type":"llm/retry","seq":0,"time":1000,"data":{}}`,
		`{"type":"turn/end","seq":1,"time":1000,"data":{"reason":{"kind":"error"}}}`,
		`{"type":"assistant/message","seq":2,"time":1000,"data":{"message":{"content":[{"type":"text","text":"another turn"}]}}}`,
	} {
		require.NoError(t, a.handleSessionEvent(stream, []byte(raw)))
	}
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestRequestContextStoresNoRowAndSetsTheWindow(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(`{"type":"request/context","seq":0,"time":1000,"data":{"contextWindow":128000}}`)))
	assert.Equal(t, int64(128000), a.streams["root"].contextWindow)
	assert.Empty(t, sink.Messages())
}

func TestCompactionIsLiveThenOneEndOnItsStreamSink(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, command := range []string{"", "command-1"} {
			t.Run(strconv.FormatBool(child)+command, func(t *testing.T) {
				base := &agenttest.Sink{}
				a := newOfflineAgent(t, base)
				stream, sink := a.streams["root"], base
				if child {
					stream = &sessionStream{sessionID: "native-child", childAgentID: "stored-child", lastSeq: -1}
					sink = base.Child("stored-child")
				}
				raw, err := json.Marshal(map[string]any{"type": "compaction/start", "seq": 0, "time": 1000, "data": map[string]any{"sourceCommandId": command}})
				require.NoError(t, err)
				require.NoError(t, a.handleSessionEvent(stream, raw))
				trigger := "auto"
				if command != "" {
					trigger = "manual"
				}
				assert.Equal(t, []map[string]interface{}{{"compaction": map[string]interface{}{"trigger": trigger}}}, sink.SessionInfos())
				assert.Empty(t, sink.Messages())
				assert.Empty(t, sink.PersistedNotifications())
				require.NoError(t, a.handleSessionEvent(stream, []byte(`{"type":"compaction/end","seq":1,"time":1000,"data":{"error":"failed"}}`)))
				require.Len(t, sink.PersistedNotifications(), 1)
				assert.Equal(t, stream.sessionID, sink.PersistedNotifications()[0].AgentSessionID)
				require.Len(t, sink.SessionInfos(), 2)
				assert.Equal(t, map[string]interface{}{"compaction": nil}, sink.SessionInfos()[1])
			})
		}
	}
}

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

type heldTranscriptSink struct {
	*agenttest.Sink
	failure error
}

func (s *heldTranscriptSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.failure != nil {
		return s.failure
	}
	return s.Sink.PersistMessage(source, content, span)
}

func (s *heldTranscriptSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if s.failure != nil {
		return false, s.failure
	}
	return s.Sink.PersistNotification(source, content)
}

func (s *heldTranscriptSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if s.failure != nil {
		return s.failure
	}
	return s.Sink.PersistTurnEnd(content, span)
}

func TestRetainedOutputKeepsEveryBlockAndReleasesTheTurnAfterPersistence(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	sink := &heldTranscriptSink{Sink: base, failure: errors.New("the transcript write failed")}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	stream := a.streams["root"]
	frames := []string{
		`{"type":"turn/start","seq":1,"time":1000,"data":{}}`,
		`{"type":"assistant/message","seq":2,"time":1001,"data":{"message":{"content":[{"type":"reasoning","text":"First thought."},{"type":"text","text":"First answer."}]}}}`,
		`{"type":"tool/call","seq":3,"time":1002,"data":{"callId":"call-1","name":"bash"}}`,
		`{"type":"turn/end","seq":4,"time":1003,"data":{"reason":{"kind":"completed"}}}`,
	}
	for _, frame := range frames {
		require.NoError(t, a.handleSessionEvent(stream, []byte(frame)))
	}
	assert.Equal(t, int64(4), stream.lastSeq, "the output queue owns every retained row")
	assert.Empty(t, base.Messages())
	assert.NotContains(t, base.TurnLifecycle(), "turn_active:false")
	sink.failure = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	rows := base.Messages()
	require.Len(t, rows, 5)
	assert.JSONEq(t, `{"blockIndex":0}`, string(rows[0].SupplementalContent))
	assert.JSONEq(t, `{"blockIndex":1}`, string(rows[1].SupplementalContent))
	assert.Equal(t, "call-1", rows[2].SpanID)
	assert.Equal(t, "call-1", rows[3].SpanID)
	assert.True(t, rows[4].TurnEnd)
	assert.Equal(t, "turn_active:false", base.TurnLifecycle()[len(base.TurnLifecycle())-1])
	for _, frame := range frames {
		require.NoError(t, a.handleSessionEvent(stream, []byte(frame)))
	}
	assert.Len(t, base.Messages(), 5, "native replay must not submit retained rows again")
}

func TestRetainedNotificationStillResolvesRetryAndCompactionState(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	sink := &heldTranscriptSink{Sink: base, failure: errors.New("the notification write failed")}
	a := newOfflineAgent(t, base)
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	stream := a.streams["root"]
	for _, frame := range []string{
		`{"type":"compaction/start","seq":1,"time":1000,"data":{}}`,
		`{"type":"compaction/end","seq":2,"time":1001,"data":{}}`,
		`{"type":"llm/retry","seq":3,"time":1002,"data":{"attempt":1}}`,
		`{"type":"assistant/message","seq":4,"time":1003,"data":{"message":{"content":[{"type":"text","text":"Recovered answer."}]}}}`,
	} {
		require.NoError(t, a.handleSessionEvent(stream, []byte(frame)))
	}
	assert.False(t, stream.compactionLive)
	value, exists := base.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
	sink.failure = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, base.PersistedNotifications(), 2)
	require.Len(t, base.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, base.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	require.Len(t, base.Messages(), 1)
	assert.Contains(t, string(base.Messages()[0].Content), "Recovered answer.")
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
// The parent agent sends a later input to a continuable child with `send_message`. The native
// process stores it as a user message with the source kind `agent-message` (dsh-subagent
// continuation-messages createAgentMessage), and LeapMux stores it nowhere else. The native
// process also injects the instruction file and the runtime context as user messages.
func TestChildUserMessagesStoreOnlyTheInputThatLeapMuxDidNotStore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		source string
		stored bool
	}{
		{"the prompt of the parent agent", `{"kind":"user"}`, true},
		{"a message that the parent agent sent", `{"kind":"agent-message","form":"relay","senderSessionId":"native-root"}`, true},
		{"a prompt that a client sent", `{"kind":"user","rpcId":"request-1"}`, false},
		{"an agent message that carries a client request id", `{"kind":"agent-message","form":"relay","senderSessionId":"native-root","rpcId":"request-1"}`, false},
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
