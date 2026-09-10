package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `turn/started` notification confirms that Codex accepted a turn.
// sendTurnStart uses it because response timing differs across Codex versions.
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

func TestCodex_MultiAgentV2ChildDoesNotAdvertiseDirectInput(t *testing.T) {
	t.Parallel()

	assert.False(t, codexProvider{}.SupportsChildSteering(),
		"Codex rejects direct app-server input for Multi-Agent V2 children")
	_, sendsDirectInput := any(&CodexAgent{}).(ChildSteerer)
	assert.False(t, sendsDirectInput)
	_, interruptsChild := any(&CodexAgent{}).(ChildInterrupter)
	assert.True(t, interruptsChild, "the app server still permits direct child interruption")
}

// TestCodex_InterruptChildUnknownThreadReturnsRetryable mirrors the send path
// for interrupt: a NotFound misclassification would make the frontend drop the
// child tab as unavailable even though the root process is running.
func TestCodex_InterruptChildUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	a := &CodexAgent{}
	err := a.InterruptChild("unknown-thread")
	assert.ErrorIs(t, err, ErrChildRouteNotReady)
}

// Codex declares `prompt` on the collabAgentToolCall thread item
// (app-server-protocol schema/typescript/v2/ThreadItem.ts). The spawn item names
// the receiver threads, but the child transcript is created later from
// agentsStates, so the prompt waits in the index until then.
func TestCodex_CollabPromptHeldUntilTheChildExists(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabChildPrompt("thread-1", "Write the essay.")

	// Spent once, so a second child creation cannot repeat it.
	assert.Equal(t, "Write the essay.", a.takeCollabChildPrompt("thread-1"))
	assert.Empty(t, a.takeCollabChildPrompt("thread-1"))
}

// The spawn's own prompt wins; the later collab tools (send/wait) on the same
// thread carry none and must not clear or replace it.
func TestCodex_CollabPromptFirstWriteWins(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabChildPrompt("thread-1", "first")
	a.rememberCollabChildPrompt("thread-1", "second")
	a.rememberCollabChildPrompt("thread-1", "")
	assert.Equal(t, "first", a.takeCollabChildPrompt("thread-1"))
}

func TestCodex_CollabPromptIgnoresEmptyInput(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabChildPrompt("", "x")
	a.rememberCollabChildPrompt("thread-1", "")
	assert.Empty(t, a.collabChildren)
}

// A completed run drops its spent prompt but keeps the route that a follow-up
// turn needs.
func TestCodex_CollabPromptDroppedOnFinalChild(t *testing.T) {
	t.Parallel()

	a := &CodexAgent{}
	a.rememberCollabChildPrompt("thread-1", "Write the essay.")
	a.finishCollabChildRun("thread-1")
	assert.Empty(t, a.takeCollabChildPrompt("thread-1"))
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

func TestCodex_AgentPathTitleUsesTheLastNonEmptySegment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "probe_child", codexAgentPathTitle("/root/probe_child"))
	assert.Equal(t, "probe_child", codexAgentPathTitle(" /root/probe_child/// "))
	assert.Equal(t, "probe_child", codexAgentPathTitle("probe_child"))
	assert.Empty(t, codexAgentPathTitle("///"))
	assert.Empty(t, codexAgentPathTitle(""))
}

func TestCodex_RootPathClassificationUsesTheCompleteCanonicalPath(t *testing.T) {
	t.Parallel()

	assert.True(t, codexAgentPathIsRoot("/root"))
	assert.True(t, codexAgentPathIsRoot(" /root/// "))
	assert.False(t, codexAgentPathIsRoot("/root/root"), "a child can use root as its task name")
	assert.False(t, codexAgentPathIsRoot("root"))
	assert.False(t, codexAgentPathIsRoot(""))
}
