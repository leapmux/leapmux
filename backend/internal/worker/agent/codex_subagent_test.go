package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

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
	a.rememberCollabPrompt("thread-1", "Write the essay.")
	assert.Equal(t, "Write the essay.", a.collabChildPrompts["thread-1"])

	// Spent once, so a second child creation cannot repeat it.
	assert.Equal(t, "Write the essay.", a.takeCollabPrompt("thread-1"))
	assert.Empty(t, a.takeCollabPrompt("thread-1"))
}

// The spawn's own prompt wins; the later collab tools (send/wait) on the same
// thread carry none and must not clear or replace it.
func TestCodex_CollabPromptFirstWriteWins(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabPrompt("thread-1", "first")
	a.rememberCollabPrompt("thread-1", "second")
	a.rememberCollabPrompt("thread-1", "")
	assert.Equal(t, "first", a.collabChildPrompts["thread-1"])
}

func TestCodex_CollabPromptIgnoresEmptyInput(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabPrompt("", "x")
	a.rememberCollabPrompt("thread-1", "")
	assert.Empty(t, a.collabChildPrompts)
}

// A terminal child drops its remembered prompt along with the rest of its index
// entries, so a long session that cycles subagents cannot accumulate them.
func TestCodex_CollabPromptDroppedOnTerminalChild(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabPrompt("thread-1", "Write the essay.")
	a.removeCollabChildIndex("thread-1")
	assert.Empty(t, a.collabChildPrompts)
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
func TestCodex_ParseCollabToolCallTolueratesNullPrompt(t *testing.T) {
	t.Parallel()

	collab := parseCollabToolCall(json.RawMessage(
		`{"type":"collabAgentToolCall","tool":"waitForAgent","receiverThreadIds":["thread-1"],"prompt":null}`))
	require.NotNil(t, collab)
	assert.Empty(t, collab.Prompt)
}
