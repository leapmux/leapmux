package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingInputReadySink struct {
	testSink
	entered chan struct{}
	release chan struct{}
}

func (s *blockingInputReadySink) InputReady() {
	close(s.entered)
	<-s.release
}

func TestCodexCompactCommandUsesNativeCompaction(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{}`)
	})
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"

	require.NoError(t, agent.SendInput(" /compact ", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "thread/compact/start", requests()[0].Method)
	assert.Equal(t, "thread-1", requests()[0].Params["threadId"])
}

func TestCodexTurnStartProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage { return json.RawMessage(`{}`) })
	agent.threadID = "thread-1"
	close(agent.processDone)

	assert.ErrorIs(t, agent.SendInput("hello", nil), ErrDeliveryUncertain)
}

func TestCodexTurnStartExplicitRejectionIsKnownFailure(t *testing.T) {
	t.Parallel()

	agent, _, _ := newCodexAgentForRPC(t, func(string) json.RawMessage {
		return json.RawMessage(`{"code":-32600,"message":"thread is active"}`)
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
	agent, _, requests := newCodexAgentForRPC(t, func(string) json.RawMessage {
		<-releaseResponse
		return json.RawMessage(`{}`)
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

	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`)))

	require.Equal(t, 1, sink.MessageCount())
	message := sink.Messages()[0]
	assert.Equal(t, "contextCompaction", message.SpanType)
	assert.Equal(t, "compact-1", message.SpanID)
	assert.Equal(t, 0, sink.InputReadyCount(), "the item boundary must not end its enclosing turn")

	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
	require.Eventually(t, func() bool {
		return sink.InputReadyCount() == 1
	}, time.Second, 10*time.Millisecond)
}

func TestHandleCodexOutput_CompactionTurnCompletionDoesNotBlockReader(t *testing.T) {
	t.Parallel()

	sink := &blockingInputReadySink{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(sink.release)
	agent := newCodexAgentWithSink(sink)
	// Bypass the thinking-token wrapper so this test isolates the output-reader
	// callback order. A separate assertion verifies wrapper forwarding.
	agent.sink = sink
	done := make(chan struct{})

	go func() {
		handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
		close(done)
	}()

	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("compaction completion did not notify the input queue")
	}

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("input queue callback blocked the Codex output reader")
	}
}
