package letta

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A resume addresses an EXISTING conversation, so runtime_start must carry
// `conversation_id` + `agent_id` and never the create_* fields. The protocol
// makes each pair mutually exclusive: a command that sets both is a shape no
// live accept. A failed lookup must not fall back to create_*, which would
// start a NEW conversation in place of the one the caller asked for.

// lettaResumeStore writes one conversation record under a temp store and
// points the process environment at it. t.Setenv requires a non-parallel test.
func lettaResumeStore(t *testing.T, id, agentID string) string {
	t.Helper()
	backend := t.TempDir()
	conv := filepath.Join(backend, lettaConversationsDir, id)
	require.NoError(t, os.MkdirAll(conv, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(conv, lettaConversationFile),
		[]byte(`{"id":"`+id+`","agent_id":"`+agentID+`"}`), 0o644))
	t.Setenv(lettaBackendDirEnv, backend)
	return backend
}

// resumeOptions returns options that resume the stated conversation.
func resumeOptions(id string) agent.Options {
	opts := newTestOptions()
	opts.ResumeSessionID = id
	return opts
}

// TestOpenConversationResumeNamesTheStoredPair pins the resume wire shape.
// runtime_start carries `conversation_id` + `agent_id` from the conversation
// record, and neither create_* field: the pairs are mutually exclusive.
func TestOpenConversationResumeNamesTheStoredPair(t *testing.T) {
	lettaResumeStore(t, "local-conv-1", "agent-local-1")
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(resumeOptions("local-conv-1")) }()

	command := fake.nextCommand(t)
	assert.Equal(t, "runtime_start", command["type"], "a resume still opens with runtime_start")
	assert.Equal(t, "local-conv-1", command["conversation_id"], "the conversation id is the resume handle")
	assert.Equal(t, "agent-local-1", command["agent_id"], "the agent id comes from the conversation record")
	assert.NotContains(t, command, "create_agent", "a resume never creates an agent")
	assert.NotContains(t, command, "create_conversation", "a resume never creates a conversation")

	fake.replies <- []byte(`{"type":"runtime_start_response","request_id":"rs-1","success":true,"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"}}`)
	select {
	case err := <-returned:
		require.NoError(t, err, "the response settles the resume handshake")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the resume response")
	}

	a.Mu.Lock()
	agentID, conversationID := a.agentID, a.conversationID
	a.Mu.Unlock()
	assert.Equal(t, "agent-local-1", agentID)
	assert.Equal(t, "local-conv-1", conversationID)
}

// TestOpenConversationResumeFailsForAnUnknownConversation pins the error path.
// A conversation id with no record behind it must fail the start. It must not
// fall back to create_*: that would open a NEW conversation in place of the
// one the caller named.
func TestOpenConversationResumeFailsForAnUnknownConversation(t *testing.T) {
	backend := t.TempDir()
	t.Setenv(lettaBackendDirEnv, backend)
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(resumeOptions("local-conv-missing")) }()

	select {
	case err := <-returned:
		require.Error(t, err, "an unknown conversation fails the resume")
		assert.Contains(t, err.Error(), "local-conv-missing", "the error names the conversation")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return for the unknown conversation")
	}
	select {
	case command := <-fake.commands:
		t.Fatalf("a failed lookup still wrote a command: %v", command)
	default:
	}
}

// TestOpenConversationResumeFailsForARecordWithNoAgent pins the second error
// path. LeapMux needs both runtime ids after the handshake, so a conversation
// record that names no agent cannot produce a scope to address input to.
func TestOpenConversationResumeFailsForARecordWithNoAgent(t *testing.T) {
	lettaResumeStore(t, "local-conv-2", "")
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(resumeOptions("local-conv-2")) }()

	select {
	case err := <-returned:
		require.Error(t, err, "a record with no agent fails the resume")
		assert.Contains(t, err.Error(), "no agent", "the error states the missing agent")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return for the agent-less record")
	}
	select {
	case command := <-fake.commands:
		t.Fatalf("a failed lookup still wrote a command: %v", command)
	default:
	}
}

// TestOpenConversationCreatePathStillCreates pins that an empty resume handle
// keeps the create path: create_agent on the wire, and no resume fields. The
// two pairs are mutually exclusive on the wire. `create_agent` alone produces
// the conversation as well, so no `create_conversation` is sent.
func TestOpenConversationCreatePathStillCreates(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(newTestOptions()) }()

	command := fake.nextCommand(t)
	assert.Equal(t, "runtime_start", command["type"])
	assert.Contains(t, command, "create_agent", "a fresh agent is created through create_agent")
	assert.NotContains(t, command, "agent_id", "the create path never names an existing agent")
	assert.NotContains(t, command, "conversation_id", "the create path never names an existing conversation")

	fake.replies <- []byte(lettaLiveRuntimeStartResponse)
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the create response")
	}
}

// TestLettaResumeAgentIDRejectsAnEmptyHandle pins the boundary check under the
// resume lookup: an empty conversation id is not a handle to look up.
func TestLettaResumeAgentIDRejectsAnEmptyHandle(t *testing.T) {
	t.Parallel()
	_, err := lettaResumeAgentID(agent.Options{HomeDir: t.TempDir()}, "  ")
	require.Error(t, err, "an empty conversation id is refused")
	assert.Contains(t, err.Error(), "empty", "the refusal states the empty id")
}

// TestLettaResumeAgentIDFailsWithoutAStore pins that an unresolvable store is
// an error, not an invitation to create. Empty HOME and an empty store env
// leave the query with no root to look in.
func TestLettaResumeAgentIDFailsWithoutAStore(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv(lettaBackendDirEnv, "")
	_, err := lettaResumeAgentID(agent.Options{}, "local-conv-1")
	require.Error(t, err, "an unresolvable store fails the lookup")
	assert.Contains(t, err.Error(), "local-conv-1", "the error names the conversation")
}
