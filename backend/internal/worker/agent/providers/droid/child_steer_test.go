package droid

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// bufferStdin captures what the agent writes to the process's stdin.
type bufferStdin struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bufferStdin) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bufferStdin) Close() error { return nil }

// frames returns each written line as its own string.
func (b *bufferStdin) frames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Split(strings.TrimSpace(b.buf.String()), "\n")
}

// newSteerAgent builds an agent with a capturing stdin and a test sink.
func newSteerAgent(t *testing.T) (*Agent, *agenttest.Sink, *bufferStdin) {
	t.Helper()
	stdin := &bufferStdin{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "worker-agent-1",
			ProviderName: "droid",
			Ctx:          ctx,
			Cancel:       cancel,
			Stdin:        stdin,
		}),
		sink: agent.NewProviderServices(sink),
	}
	a.Mu.Lock()
	a.sessionID = "main-session"
	a.Mu.Unlock()
	return a, sink, stdin
}

// lastRequest returns the params of the last request the agent wrote.
func lastRequest(t *testing.T, stdin *bufferStdin) map[string]any {
	t.Helper()
	frames := stdin.frames()
	require.NotEmpty(t, frames, "the agent wrote no frame")
	var env struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(frames[len(frames)-1]), &env))
	var params map[string]any
	require.NoError(t, json.Unmarshal(env.Params, &params))
	return params
}

// A send writes droid.add_user_message with the child's session id and
// queuePlacement "end_of_turn".
func TestSendChildInputWritesEndOfTurn(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)

	require.NoError(t, a.SendChildInput("child-1", "hello", nil))
	params := lastRequest(t, stdin)
	assert.Equal(t, "child-1", params["sessionId"], "the message addresses the child session")
	assert.Equal(t, "hello", params["text"])
	assert.Equal(t, "end_of_turn", params["queuePlacement"], "a send queues for the child's next turn")
	assert.True(t, a.ActiveChildTurnState("child-1").Active, "a send arms the child's turn")
}

// A send to a running child refuses with ErrAgentBusy so the LeapMux queue
// holds the message.
func TestSendChildInputRefusesABusyChild(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	a.armChildTurn("child-1")

	err := a.SendChildInput("child-1", "hello", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy, "a running child refuses a new message")
	frames := stdin.frames()
	assert.Empty(t, strings.TrimSpace(frames[0]), "a refused message writes no request")
	assert.True(t, a.ActiveChildTurnState("child-1").Steerable,
		"a busy child is steerable, so the queue offers the message as a steer")
}

// A steer writes queuePlacement "end_of_loop" and needs a running turn.
func TestSteerChildInputWritesEndOfLoop(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	a.armChildTurn("child-1")

	require.NoError(t, a.SteerChildInput("child-1", "more", nil))
	params := lastRequest(t, stdin)
	assert.Equal(t, "child-1", params["sessionId"])
	assert.Equal(t, "end_of_loop", params["queuePlacement"], "a steer injects at the child's next interruption point")
	assert.True(t, a.ActiveChildTurnState("child-1").Active, "a steer does not disarm the turn")
}

// A steer to an idle child refuses with ErrNoActiveTurn.
func TestSteerChildInputRefusesAnIdleChild(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)

	err := a.SteerChildInput("child-1", "more", nil)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn, "a steer needs a running turn")
	frames := stdin.frames()
	assert.Empty(t, strings.TrimSpace(frames[0]), "a refused steer writes no request")
	assert.False(t, a.ActiveChildTurnState("child-1").Active)
}

// A send with no child key is refused before any request.
func TestSendChildInputRefusesAnEmptyKey(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)

	err := a.SendChildInput("", "hello", nil)
	require.Error(t, err, "an empty child key is refused")
	frames := stdin.frames()
	assert.Empty(t, strings.TrimSpace(frames[0]), "a refused message writes no request")
}

// The turn state of one child does not affect another.
func TestActiveChildTurnStateIsPerChild(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	a.armChildTurn("child-1")

	assert.True(t, a.ActiveChildTurnState("child-1").Active)
	assert.False(t, a.ActiveChildTurnState("child-2").Active, "another child is idle")

	a.disarmChildTurn("child-1")
	assert.False(t, a.ActiveChildTurnState("child-1").Active, "a disarm clears the flag")
}

// child_session_available arms the child's turn and persists the announcement.
func TestChildSessionAvailableArmsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":"child-1","toolUseId":"tu-1","subagentType":"explore","description":"Inspect the parser"}}}`))
	assert.True(t, a.ActiveChildTurnState("child-1").Active,
		"a spawned child starts running")
	assert.NotEmpty(t, sink.PersistedNotifications(), "the announcement is persisted for the browser")
}

// A turn end for a child session disarms that child, not the main turn.
func TestAgentTurnCompletedForAChildDisarmsTheChild(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.armTurn()
	a.armChildTurn("child-1")

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"t-1","reason":"end_turn"}}}`))
	assert.False(t, a.ActiveChildTurnState("child-1").Active, "the child's turn ends")
	assert.True(t, func() bool { a.Mu.Lock(); defer a.Mu.Unlock(); return a.turnActive }(),
		"the main turn is not touched by a child's turn end")
	assert.NotEmpty(t, sink.PersistedNotifications(), "the turn end is persisted")
}

// A turn end for the main session disarms the main turn as before.
func TestAgentTurnCompletedForTheMainSessionDisarmsTheTurn(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	a.armTurn()

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"t-1","reason":"end_turn"}}}`))
	assert.False(t, func() bool { a.Mu.Lock(); defer a.Mu.Unlock(); return a.turnActive }(),
		"the main turn ends")
}

// The child steering wires its attachments into the text field.
func TestSendChildInputJoinsAttachments(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)

	attachments := []*leapmuxv1.Attachment{
		{Data: []byte("notes")},
		{MimeType: "image/png", Filename: "shot.png"},
	}
	require.NoError(t, a.SendChildInput("child-1", "hello", attachments))
	params := lastRequest(t, stdin)
	text, _ := params["text"].(string)
	assert.Contains(t, text, "hello")
	assert.Contains(t, text, "notes")
	assert.Contains(t, text, "[image: shot.png]", "an image is a filename reference in the text field")
}
