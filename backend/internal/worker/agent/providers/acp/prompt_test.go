package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPPromptCanStartAfterTheSessionChanges(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Old session input", nil))
	cleared := make(chan error, 1)
	go func() {
		_, err := a.ClearContext()
		cleared <- err
	}()
	testutil.RequireEventually(t, func() bool {
		return a.IsPendingForTest(int64(2))
	})
	a.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"new-session"}}`)))
	require.NoError(t, <-cleared)
	require.NoError(t, a.SendInput("New session input", nil))
}

func TestACPPromptIgnoresAResponseFromThePreviousSession(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Current session input", nil))
	a.finishPromptRequest("previous-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, sink.Messages())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
	require.ErrorIs(t, a.SendInput("Later input", nil), agent.ErrAgentBusy)
}

func TestACPPromptPreservesTheCompleteNativeResultWrapper(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	original := json.RawMessage(` {"id":"native-result", "role":"result", "future":9007199254740993, "content":{"stopReason":"end_turn","usage":{"totalTokens":0}}} `)
	a.handleACPPromptResponse(original)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, []byte(original), sink.Messages()[0].Content)
	assert.True(t, sink.Messages()[0].TurnEnd)
}

// promptEnd is one call of Hooks.PromptEnded.
type promptEnd struct {
	err     error
	stopped bool
}

// recordPromptEnds sets Hooks.PromptEnded to record each call.
func recordPromptEnds(b *Base) *[]promptEnd {
	var ends []promptEnd
	b.hooks.PromptEnded = func(err error, stopped bool) {
		ends = append(ends, promptEnd{err: err, stopped: stopped})
	}
	return &ends
}

// The hook reads each end of a prompt of the current session before the base
// writes its row or its note, so a provider can settle what it holds for the
// prompt.
func TestACPPromptEndedReadsEachEndBeforeTheBaseWritesIt(t *testing.T) {
	t.Parallel()
	failure := &providerkit.JSONRPCResponseError{Code: -32000, Message: "Rate exceeded"}
	for _, tc := range []struct {
		name        string
		response    json.RawMessage
		err         error
		interrupted bool
		// agentStopped states that the agent process stopped, as a Stop leaves it.
		agentStopped bool
		want         promptEnd
	}{
		{name: "a result", response: json.RawMessage(`{"stopReason":"end_turn"}`), want: promptEnd{}},
		{name: "an error", err: failure, want: promptEnd{err: failure}},
		{name: "an error of a stopped prompt", err: failure, interrupted: true, want: promptEnd{err: failure, stopped: true}},
		{name: "a result of a stopped prompt", response: json.RawMessage(`{"stopReason":"cancelled"}`), interrupted: true, want: promptEnd{stopped: true}},
		{name: "an error of a stopped agent", err: failure, agentStopped: true, want: promptEnd{err: failure, stopped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			var ends []promptEnd
			written := -1
			b.hooks.PromptEnded = func(err error, stopped bool) {
				written = len(sink.Messages()) + sink.NotificationCount()
				ends = append(ends, promptEnd{err: err, stopped: stopped})
			}
			b.promptActive = true
			if tc.interrupted {
				b.noteACPInterruptRequested()
			}
			b.SetStoppedForTest(tc.agentStopped)

			b.finishPromptRequest("session-1", tc.response, tc.err)

			assert.Equal(t, []promptEnd{tc.want}, ends)
			assert.Zero(t, written, "the hook runs before the base writes the end")
		})
	}
}

func TestACPPromptEndedSkipsAResponseOfThePreviousSession(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	ends := recordPromptEnds(b)

	b.finishPromptRequest("previous-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Empty(t, *ends, "the prompt of a replaced session is no prompt of the current one")
}

// The conversation that a resumed session replays before its session/load reply
// is history the Worker already stores as its own transcript rows, so the base
// drops it. Goose, Reasonix, Fast Agent and Dirac all replay inside the load
// handler, before the reply; persisting their replay drew every stored row a
// second time beside the copy the Worker restored.
func TestResumeReplayBeforeTheLoadReplyIsDropped(t *testing.T) {
	t.Parallel()
	peer := &handshakePeer{linesBefore: map[string][]string{
		MethodSessionLoad: {
			string(acptest.Chunk("stored-1", contracts.ACPUpdateUserMessageChunk, "OLDPROMPT")),
			string(acptest.Chunk("stored-1", contracts.ACPUpdateAgentMessageChunk, "OLD")),
			string(acptest.Chunk("stored-1", contracts.ACPUpdateAgentThoughtChunk, "OLDTHOUGHT")),
		},
		MethodSessionPrompt: {string(acptest.Chunk("stored-1", contracts.ACPUpdateAgentMessageChunk, "NEW"))},
	}}
	b, sink, session := runHandshakeForTest(t, peer, Hooks{},
		agent.Options{WorkingDir: "/work", ResumeSessionID: "stored-1"},
		SessionConfig{NewMethod: MethodSessionNew, ResumeMethod: MethodSessionLoad},
		func(method string) json.RawMessage {
			switch method {
			case MethodInitialize:
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
			case MethodSessionPrompt:
				return json.RawMessage(`{"stopReason":"end_turn"}`)
			default:
				return json.RawMessage(`{}`)
			}
		})
	require.Equal(t, "stored-1", session.SessionID)
	// Start drains the handshake buffer after the startup settings apply. The
	// replay below the load reply is gone, so no row holds the old conversation.
	b.finishSessionUpdates()
	assert.Empty(t, sink.Messages(), "the replay a load reply covers persists no row")

	require.NoError(t, b.SendInput("NEXT", nil))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	messages := sink.Messages()
	assert.Equal(t, []string{"text:NEW"}, assembledTexts(t, messages))
	require.NotEmpty(t, messages)
	assert.True(t, messages[len(messages)-1].TurnEnd, "the turn end follows the new segment alone")
}

// A chunk that arrives after the session/load reply is live output of the
// resumed session, not replay, so it still persists as its own segment even
// though it was buffered while the startup settings applied: an OpenCode or
// Kilo idle stream can run through the rest of the startup.
func TestLiveTextAfterTheLoadReplyIsKept(t *testing.T) {
	t.Parallel()
	peer := &handshakePeer{linesBefore: map[string][]string{
		MethodSessionLoad: {
			string(acptest.Chunk("stored-1", contracts.ACPUpdateAgentMessageChunk, "OLD")),
		},
		MethodSessionPrompt: {string(acptest.Chunk("stored-1", contracts.ACPUpdateAgentMessageChunk, "NEW"))},
	}}
	b, sink, session := runHandshakeForTest(t, peer, Hooks{},
		agent.Options{WorkingDir: "/work", ResumeSessionID: "stored-1"},
		SessionConfig{NewMethod: MethodSessionNew, ResumeMethod: MethodSessionLoad},
		func(method string) json.RawMessage {
			switch method {
			case MethodInitialize:
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
			case MethodSessionPrompt:
				return json.RawMessage(`{"stopReason":"end_turn"}`)
			default:
				return json.RawMessage(`{}`)
			}
		})
	require.Equal(t, "stored-1", session.SessionID)
	// The load reply arrived, so what follows it is conversation of this session.
	b.HandleOutput(acptest.Chunk("stored-1", contracts.ACPUpdateAgentMessageChunk, "LATE"))
	b.finishSessionUpdates()
	require.NoError(t, b.SendInput("NEXT", nil))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	// The turn start stored the live chunk as a segment of its own, the reply
	// stored the new answer, and the replay below the reply stored nothing.
	assert.Equal(t, []string{"text:LATE", "text:NEW"}, assembledTexts(t, sink.Messages()))
}

// A chunk that trails the end of a turn reaches no turn. It is a segment of its
// own, and the next prompt does not start with it. A thought is the same.
func TestPromptDoesNotInheritATrailingSegment(t *testing.T) {
	t.Parallel()
	for name, updateType := range map[string]string{
		"text":    contracts.ACPUpdateAgentMessageChunk,
		"thought": contracts.ACPUpdateAgentThoughtChunk,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := &agenttest.Stdin{}
			b, sink := newACPTurnBase(t, out)
			require.NoError(t, b.SendInput("first", nil))
			b.HandleOutput(acptest.Chunk("session-1", updateType, "FIRST"))
			b.HandleJSONRPCResponseForTest(promptResponse(1))
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

			b.HandleOutput(acptest.Chunk("session-1", updateType, "TRAIL"))
			require.NoError(t, b.SendInput("second", nil))
			b.HandleOutput(acptest.Chunk("session-1", updateType, "SECOND"))
			b.HandleJSONRPCResponseForTest(promptResponse(2))
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

			prefix := name + ":"
			assert.Equal(t, []string{prefix + "FIRST", prefix + "TRAIL", prefix + "SECOND"}, assembledTexts(t, sink.Messages()))
		})
	}
}

// A tool call that completed while no turn ran already has its row. The next
// turn-end row counts the tools of its own turn only.
func TestPromptDoesNotCountAToolThatCompletedBeforeItStarted(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"idle-tool","title":"Read","kind":"read","status":"completed"}`))
	require.Len(t, closingRows(sink, "idle-tool"), 1, "the idle tool call keeps its row")

	require.NoError(t, b.SendInput("next", nil))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"turn-tool","title":"Read","kind":"read","status":"completed"}`))
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	var counts []string
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			counts = append(counts, string(message.Metadata))
		}
	}
	require.Len(t, counts, 1)
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":1}`, counts[0], "the turn counts its own tool call only")
}

// A prompt that starts with nothing buffered writes nothing and reports nothing.
func TestPromptWithNothingBufferedStoresNothingAtItsStart(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)

	require.NoError(t, b.SendInput("hello", nil))

	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ProgressUpdates())
}

// The reader can hand a chunk to the base while a prompt starts. The chunk goes
// either to the segment before the prompt or to the prompt, and never to both
// or to neither.
func TestPromptStartKeepsEachConcurrentChunkOnce(t *testing.T) {
	t.Parallel()
	const chunks = 200
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	streamed := make(chan struct{})
	go func() {
		defer close(streamed)
		for range chunks {
			b.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "a"))
		}
	}()
	require.NoError(t, b.SendInput("hello", nil))
	<-streamed
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	texts := assembledTexts(t, sink.Messages())
	require.NotEmpty(t, texts)
	assert.LessOrEqual(t, len(texts), 2, "one segment before the prompt at most, and the prompt's own")
	stored := ""
	for _, text := range texts {
		segment, isText := strings.CutPrefix(text, "text:")
		require.True(t, isText, text)
		stored += segment
	}
	assert.Equal(t, strings.Repeat("a", chunks), stored, "each chunk is stored exactly once")
}
