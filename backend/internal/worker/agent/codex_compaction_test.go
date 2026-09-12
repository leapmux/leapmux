package agent

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexCompactContextUsesNativeCompaction(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"

	require.NoError(t, agent.CompactContext())
	require.Len(t, requests(), 1)
	assert.Equal(t, "thread/compact/start", requests()[0].Method)
	assert.Equal(t, "thread-1", requests()[0].Params["threadId"])
}

// SendInput must not read a command out of the message text. The queue
// classifies "/compact" and "/summarize" before dispatch and calls
// CompactContext instead, so CompactContext is the single entry point to a
// native compaction. The second check that lived here disagreed with that one:
// the queue classifies only a plain user message, so a control response whose
// text is "/compact" reached SendInput and started a compaction in place of the
// answer that the agent waited for.
func TestCodexSendInputDoesNotInterceptCompactCommand(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"

	result := make(chan error, 1)
	go func() { result <- agent.SendInput(" /compact ", nil) }()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)

	assert.Equal(t, "turn/start", requests()[0].Method,
		"the text must start a turn, not a native compaction")
	inputJSON, err := json.Marshal(requests()[0].Params["input"])
	require.NoError(t, err)
	assert.Contains(t, string(inputJSON), "/compact",
		"the composer text must reach the agent unchanged")

	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1"}}}`)))
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("turn/started did not confirm delivery")
	}
}

func TestCodexTurnStartProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload { return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)} })
	agent.threadID = "thread-1"
	close(agent.processDone)

	assert.ErrorIs(t, agent.SendInput("hello", nil), ErrDeliveryUncertain)
}

func TestCodexTurnStartExplicitRejectionIsKnownFailure(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		return jsonrpcResponsePayload{Error: json.RawMessage(`{"code":-32600,"message":"thread is active"}`)}
	})
	agent.threadID = "thread-1"

	err := agent.SendInput("hello", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrDeliveryUncertain)
	assert.Contains(t, err.Error(), "thread is active")
}

func TestCodexCompactionResponseTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	err := classifyCodexCompactionRequestError(errors.New("timeout waiting for thread/compact/start response"))

	assert.ErrorIs(t, err, ErrDeliveryUncertain)
	known := classifyCodexCompactionRequestError(&jsonRPCResponseError{Code: -32600, Message: "thread is active"})
	assert.NotErrorIs(t, known, ErrDeliveryUncertain)
}

func TestCodexCompactionStartNotificationConfirmsDelivery(t *testing.T) {
	t.Parallel()

	releaseResponse := make(chan struct{})
	defer close(releaseResponse)
	agent, _, requests := newCodexAgentForRPC(t, func(string) jsonrpcResponsePayload {
		<-releaseResponse
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"
	result := make(chan error, 1)
	go func() {
		result <- agent.CompactContext()
	}()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)

	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`)))

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("contextCompaction start did not confirm delivery")
	}
}

func TestHandleCodexOutput_ContextCompactionCompletionPersistsBoundary(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	completion := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`
	handleCodexOutput(agent, parseLine([]byte(completion)))

	// The completion must persist as a NOTIFICATION. persistMessage clears the
	// agent's notification thread, so a message can never join the thread that
	// the matching item/started opened, and the consolidator never drops the
	// "Compacting context..." status. The status then stays for the session.
	assert.Equal(t, 0, sink.MessageCount(),
		"the compaction boundary must not land in the transcript as a chat row")
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	assert.JSONEq(t, completion, string(last.Content),
		"the raw JSON-RPC envelope must reach the frontend unchanged")
	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindCompactionBoundary,
		Key:  "codex:item/completed:contextCompaction",
	}, codexProvider{}.Classify(last.Content),
		"the consolidator drops the compacting status only for a compaction boundary")

	assert.Empty(t, sink.TurnActives(), "the item boundary must not end its enclosing turn")

	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
	assert.Equal(t, []bool{false}, sink.TurnActives(),
		"the turn's own completion releases the Worker's input queue")
}

// The Codex app-server still emits thread/compacted for an auto-compaction. It
// belongs to codexSystemMetadataMethods, so it persists verbatim as a
// threadable agent notification. Without that entry it falls to the default
// branch and lands in the transcript as a raw JSON-RPC bubble.
func TestHandleCodexOutput_ThreadCompactedPersistsRawAsAgent(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/compacted","params":{"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Equal(t, 0, sink.MessageCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"thread/compacted must persist as AGENT (Codex-emitted)")
	assert.JSONEq(t, input, string(last.Content),
		"the raw JSON-RPC envelope must be preserved verbatim -- the frontend keys off method:\"thread/compacted\"")
	assert.False(t, codexProvider{}.Classify(last.Content).Consolidatable(),
		"item/completed is the compaction boundary now; thread/compacted stays a plain threadable notification")
}

// blockingTurnFlagSink blocks inside SetTurnState until the test releases it,
// which is what tells a synchronous publish apart from one a goroutine carries.
// Asserting the recorded order alone cannot: a spawned goroutine usually wins
// the race to the slice, so the same sequence appears either way.
type blockingTurnFlagSink struct {
	testSink
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTurnFlagSink) SetTurnState(state TurnState, seq uint64) {
	if !state.Active {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	s.testSink.SetTurnState(state, seq)
}

func TestHandleCodexOutput_TurnCompletionPublishesTheClearBeforeItReturns(t *testing.T) {
	t.Parallel()

	// The Worker's input queue takes its turn state from this publish, and the
	// state carries no turn identity -- so ORDER is the only thing that keeps
	// the clear attached to the turn it belongs to. A clear that a goroutine
	// carries can land after the NEXT turn already opened, and the queue then
	// dispatches the next message into that running turn.
	//
	// This site once did carry it that way, to keep the reader free for the
	// response to a compaction the app-server submits before it answers. The
	// publish is safe inline instead, because Manager.drain never holds the
	// coordinator lock across dispatcher.Dispatch.
	sink := &blockingTurnFlagSink{entered: make(chan struct{}), release: make(chan struct{})}
	agent := newCodexAgentWithSink(sink)
	// Bypass the thinking-token wrapper so this test isolates the output-reader
	// callback order. A separate assertion verifies wrapper forwarding.
	agent.sink = sink

	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))
	returned := make(chan struct{})
	go func() {
		handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
		close(returned)
	}()

	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("the turn end published no clear")
	}
	select {
	case <-returned:
		t.Fatal("the handler returned before the clear reached the sink, so a new goroutine carried it")
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.release)
	<-returned

	assert.Equal(t, []bool{true, false}, sink.TurnActives(),
		"the handler reports the end before it returns, so the reader's next line cannot overtake it")
}
