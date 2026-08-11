package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Codex's ACK for a `turn/start` is the `turn/started` notification, and
// `sendTurnStart` waits on exactly that.
//
// The turn/start RESPONSE cannot serve: Codex answers it when the turn ENDS,
// minutes or hours later. Waiting on it held the worker's RPC ack and the
// user-message broadcast for the whole turn, so the browser -- whose deadline is
// 15s -- labelled a message it had already delivered "Failed to deliver".
func TestCodex_TurnStartedIsTheAckThatReleasesASend(t *testing.T) {
	t.Parallel()
	ack := make(chan struct{})
	a := &CodexAgent{turnStartAck: ack}
	a.threadID = "t-1"
	a.sink = noopSink{}

	a.handleTurnStarted(json.RawMessage(`{"threadId":"t-1","turn":{"id":"turn-1"}}`))

	select {
	case <-ack:
	default:
		t.Fatal("turn/started must release the waiting send")
	}
	assert.Nil(t, a.turnStartAck, "the waiter is cleared, so a later turn cannot close it twice")
	assert.Equal(t, "turn-1", a.turnID, "and the turn id it carries is what makes the next send steer")
}

// A CHILD's turn/started must not release the primary send: it belongs to a
// collab subagent thread, and the main thread has not accepted anything yet.
func TestCodex_AChildTurnStartedDoesNotReleaseTheSend(t *testing.T) {
	t.Parallel()
	ack := make(chan struct{})
	a := &CodexAgent{turnStartAck: ack}
	a.threadID = "t-main"
	a.sink = noopSink{}

	a.handleTurnStarted(json.RawMessage(`{"threadId":"t-child","turn":{"id":"turn-child"}}`))

	select {
	case <-ack:
		t.Fatal("a child's turn must not acknowledge the main thread's send")
	default:
	}
	assert.NotNil(t, a.turnStartAck, "the main send is still waiting")
}

// TestCodex_SendChildInputUnknownThreadReturnsRetryable verifies that when the
// owner process is running but its in-memory collab index does not know the
// thread (empty after a worker restart), SendChildInput wraps the failure in
// ErrChildNotSteerableYet. The service maps that to UNAVAILABLE so the client
// re-queues, instead of persisting a permanent delivery error.
func TestCodex_SendChildInputUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	// A fresh CodexAgent has an empty collabThreadSpans (the post-restart
	// state until the live spawn re-fires).
	a := &CodexAgent{}
	err := a.SendChildInput("unknown-thread", "hello", []*leapmuxv1.Attachment{})
	assert.ErrorIs(t, err, ErrChildNotSteerableYet,
		"an unknown thread on a running owner must be retryable, not a hard failure")
}

// TestCodex_InterruptChildUnknownThreadReturnsRetryable mirrors the send path
// for interrupt: a NotFound misclassification would make the frontend drop the
// child tab as unavailable even though the root process is running.
func TestCodex_InterruptChildUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	a := &CodexAgent{}
	err := a.InterruptChild("unknown-thread")
	assert.ErrorIs(t, err, ErrChildNotSteerableYet)
}

// Codex declares `prompt` on the collabAgentToolCall thread item
// (app-server-protocol schema/typescript/v2/ThreadItem.ts). The spawn item names
// the receiver threads, but the child transcript is created later from
// agentsStates, so the prompt waits in the index until then.
func TestCodex_CollabPromptHeldUntilTheChildExists(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.collabChildPrompts.remember("thread-1", "Write the essay.")
	assert.Equal(t, "Write the essay.", a.collabChildPrompts.peek("thread-1"))

	// Spent once, so a second child creation cannot repeat it.
	assert.Equal(t, "Write the essay.", a.collabChildPrompts.take("thread-1"))
	assert.Empty(t, a.collabChildPrompts.take("thread-1"))
}

// The spawn's own prompt wins; the later collab tools (send/wait) on the same
// thread carry none and must not clear or replace it.
func TestCodex_CollabPromptFirstWriteWins(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.collabChildPrompts.remember("thread-1", "first")
	a.collabChildPrompts.remember("thread-1", "second")
	a.collabChildPrompts.remember("thread-1", "")
	assert.Equal(t, "first", a.collabChildPrompts.peek("thread-1"))
}

func TestCodex_CollabPromptIgnoresEmptyInput(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.collabChildPrompts.remember("", "x")
	a.collabChildPrompts.remember("thread-1", "")
	assert.Zero(t, a.collabChildPrompts.count())
}

// A terminal child drops its remembered prompt along with the rest of its index
// entries, so a long session that cycles subagents cannot accumulate them.
func TestCodex_CollabPromptDroppedOnFinalChild(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.collabChildPrompts.remember("thread-1", "Write the essay.")
	a.removeCollabChildIndex("thread-1")
	assert.Zero(t, a.collabChildPrompts.count())
}

// The spawn item's prompt reaches the index through the parse.
func TestCodex_ParseCollabToolCallCarriesThePrompt(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"spawnAgent","status":"inProgress",` +
			`"receiverThreadIds":["thread-1"],"prompt":"Write the essay.","model":null}`))
	require.NotNil(t, collab)
	assert.Equal(t, "Write the essay.", collab.Prompt)
	assert.Equal(t, []string{"thread-1"}, collab.ReceiverThreadIds)
}

// `prompt` is nullable on the wire for the non-spawn collab tools.
func TestCodex_ParseCollabToolCallToleratesNullPrompt(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"waitForAgent","receiverThreadIds":["thread-1"],"prompt":null}`))
	require.NotNil(t, collab)
	assert.Empty(t, collab.Prompt)
}
