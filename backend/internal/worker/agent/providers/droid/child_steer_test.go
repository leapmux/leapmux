package droid

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// bufferStdin captures what the agent writes to the process's stdin.
type bufferStdin struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
	respond func(droidEnvelope)
}

func (b *bufferStdin) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	if b.written != nil {
		select {
		case b.written <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
	if b.respond != nil {
		var request droidEnvelope
		if json.Unmarshal(p, &request) == nil {
			b.respond(request)
		}
	}
	return n, err
}

func (b *bufferStdin) Close() error { return nil }

// frames returns the captured lines, or no entries when nothing was written.
func (b *bufferStdin) frames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(b.buf.String()), "\n")
}

func TestBufferStdinFramesDistinguishesNoWritesFromABlankLine(t *testing.T) {
	t.Parallel()
	stdin := &bufferStdin{}
	assert.Empty(t, stdin.frames(), "an untouched stream contains no native input frame")
	_, err := stdin.Write([]byte("\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{""}, stdin.frames(), "a real blank-line write must remain observable")
}

// newSteerAgent builds an agent with a capturing stdin and a test sink.
func newSteerAgent(t *testing.T) (*Agent, *agenttest.Sink, *bufferStdin) {
	t.Helper()
	return newSteerAgentWithProcessDone(t, make(chan struct{}))
}

func newSteerAgentWithProcessDone(t *testing.T, processDone chan struct{}) (*Agent, *agenttest.Sink, *bufferStdin) {
	t.Helper()
	stdin := &bufferStdin{written: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		select {
		case <-processDone:
		default:
			close(processDone)
		}
		cancel()
	})
	sink := &agenttest.Sink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "worker-agent-1",
			ProviderName: "droid",
			Ctx:          ctx,
			Cancel:       cancel,
			Stdin:        stdin,
			ProcessDone:  processDone,
		}),
		sink: agent.NewProviderServices(sink),
	}
	a.Mu.Lock()
	a.sessionID = "main-session"
	a.Mu.Unlock()
	stdin.respond = func(request droidEnvelope) {
		if request.Method == droidMethodAddUserMessage {
			answerDroidInput(t, a, request.ID, false)
		}
	}
	return a, sink, stdin
}

func TestSteerAgentCleanupReleasesStop(t *testing.T) {
	t.Parallel()
	stopped := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Error("the fake process did not report completion at test cleanup")
		}
	})
	a, _, _ := newSteerAgent(t)
	go func() {
		a.Stop()
		close(stopped)
	}()
}

const steerChildID = "52af9b7b-5e82-4932-83c0-a2bf1caa2fcd"

// newBoundChildAgent gives the root and child separate stream writers.
func newBoundChildAgent(t *testing.T) (*Agent, *Agent, *bufferStdin, *bufferStdin) {
	t.Helper()
	root, sink, rootStdin := newSteerAgent(t)
	child, _, childStdin := newSteerAgent(t)
	child.Mu.Lock()
	child.sessionID = steerChildID
	child.Mu.Unlock()
	childAgentID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "droid-tool-task-1", ProviderChildKey: steerChildID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: steerChildID, Kind: bgtask.KindSubagent, ChildAgentID: childAgentID,
		ParentAgentID: root.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
	}))
	ready := make(chan struct{})
	close(ready)
	root.childConns = map[string]*droidChildConnection{steerChildID: {ready: ready, agent: child}}
	return root, child, rootStdin, childStdin
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
	a, _, rootStdin, childStdin := newBoundChildAgent(t)

	require.NoError(t, a.SendChildInput(steerChildID, "hello", nil))
	params := lastRequest(t, childStdin)
	assert.Equal(t, steerChildID, params["sessionId"], "the message addresses the loaded child session")
	assert.Equal(t, "hello", params["text"])
	assert.Equal(t, "end_of_turn", params["queuePlacement"], "a send queues for the child's next turn")
	assert.True(t, a.ActiveChildTurnState(steerChildID).Active, "a send arms the child's turn")
	assert.Empty(t, rootStdin.frames(), "the root stream receives no child input")
}

// A send to a running child refuses with ErrAgentBusy so the LeapMux queue
// holds the message.
func TestSendChildInputRefusesABusyChild(t *testing.T) {
	t.Parallel()
	a, child, _, childStdin := newBoundChildAgent(t)
	child.startTurn()

	err := a.SendChildInput(steerChildID, "hello", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy, "a running child refuses a new message")
	assert.Empty(t, childStdin.frames(), "a refused message writes no request")
	assert.True(t, a.ActiveChildTurnState(steerChildID).Steerable,
		"a busy child is steerable, so the queue offers the message as a steer")
}

func TestSendChildInputWaitsForAnUnboundBackgroundChild(t *testing.T) {
	t.Parallel()
	a, sink, rootStdin := newSteerAgent(t)
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "droid-tool-task-1", ProviderChildKey: steerChildID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: steerChildID, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusRunning,
	}))
	a.armChildTurn(steerChildID)
	assert.ErrorIs(t, a.SendChildInput(steerChildID, "hello", nil), agent.ErrAgentBusy)
	state := a.ActiveChildTurnState(steerChildID)
	assert.True(t, state.Active)
	assert.False(t, state.Steerable, "the root stream cannot steer the running child")
	assert.Empty(t, rootStdin.frames())
}

// A steer writes Droid's active-turn placement and needs a running turn.
func TestSteerChildInputWritesEndOfTurn(t *testing.T) {
	t.Parallel()
	a, child, _, childStdin := newBoundChildAgent(t)
	child.startTurn()

	require.NoError(t, a.SteerChildInput(steerChildID, "more", nil))
	params := lastRequest(t, childStdin)
	assert.Equal(t, steerChildID, params["sessionId"])
	assert.Equal(t, "end_of_turn", params["queuePlacement"], "Droid processes this placement during a running child turn")
	assert.True(t, a.ActiveChildTurnState(steerChildID).Active, "a steer does not disarm the turn")
}

// A steer to an idle child refuses with ErrNoActiveTurn.
func TestSteerChildInputRefusesAnIdleChild(t *testing.T) {
	t.Parallel()
	a, _, _, childStdin := newBoundChildAgent(t)

	err := a.SteerChildInput(steerChildID, "more", nil)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn, "a steer needs a running turn")
	assert.Empty(t, childStdin.frames(), "a refused steer writes no request")
	assert.False(t, a.ActiveChildTurnState(steerChildID).Active)
}

// A send with no child key is refused before any request.
func TestSendChildInputRefusesAnEmptyKey(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)

	err := a.SendChildInput("", "hello", nil)
	require.Error(t, err, "an empty child key is refused")
	assert.Empty(t, stdin.frames(), "a refused message writes no request")
}

func TestChildInputRefusesAnUnregisteredSession(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	const unknown = "52af9b7b-5e82-4932-83c0-a2bf1caa2fcd"
	assert.ErrorIs(t, a.SendChildInput(unknown, "hello", nil), agent.ErrChildRouteNotReady)
	a.armChildTurn(unknown)
	assert.ErrorIs(t, a.SteerChildInput(unknown, "guide", nil), agent.ErrChildRouteNotReady)
	assert.Empty(t, stdin.frames(), "an unregistered child cannot use the root stream")
}

// The turn state of one child does not affect another.
func TestActiveChildTurnStateIsPerChild(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	a.armChildTurn("child-1")

	assert.True(t, a.ActiveChildTurnState("child-1").Active)
	assert.False(t, a.ActiveChildTurnState("child-1").Steerable, "the root cannot steer a background child")
	assert.False(t, a.ActiveChildTurnState("child-2").Active, "another child is idle")

	a.disarmChildTurn("child-1")
	assert.False(t, a.ActiveChildTurnState("child-1").Active, "a disarm clears the flag")
}

// child_session_available starts the child turn and updates its registry row.
func TestChildSessionAvailableArmsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":"child-1","toolUseId":"tu-1","subagentType":"explorer","description":"Inspect the parser"}}}`))
	assert.True(t, a.ActiveChildTurnState("child-1").Active,
		"a spawned child starts running")
	assert.Empty(t, sink.PersistedNotifications(), "the registry carries the child announcement")
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	assert.NotEmpty(t, row.ChildAgentID)
}

// A turn end for a child session disarms that child, not the main turn.
func TestAgentTurnCompletedForAChildDisarmsTheChild(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.startTurn()
	a.HandleOutput([]byte(droidChildAvailable))
	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"t-1","reason":"completed"}}}`))
	assert.False(t, a.ActiveChildTurnState("child-1").Active, "the child's turn ends")
	assert.True(t, func() bool { a.Mu.Lock(); defer a.Mu.Unlock(); return a.turnActive }(),
		"the main turn is not touched by a child's turn end")
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.NotEmpty(t, childRows)
	assert.True(t, childRows[len(childRows)-1].TurnEnd, "the turn end stays in the child transcript")
}

// A turn end for the main session disarms the main turn as before.
func TestAgentTurnCompletedForTheMainSessionDisarmsTheTurn(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	a.startTurn()

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"t-1","reason":"completed"}}}`))
	assert.False(t, func() bool { a.Mu.Lock(); defer a.Mu.Unlock(); return a.turnActive }(),
		"the main turn ends")
}

// The child sends text, image bytes, and PDF bytes through its own process.
func TestSendChildInputCarriesAttachments(t *testing.T) {
	t.Parallel()
	a, child, rootStdin, childStdin := newBoundChildAgent(t)
	installDroidPDFCatalog(t, child, "child-pdf-model", []map[string]any{{"id": "child-pdf-model", "supportsPDFs": true}})

	attachments := []*leapmuxv1.Attachment{
		{Data: []byte("notes")},
		{MimeType: "image/png", Filename: "shot.png", Data: []byte{0x89, 'P', 'N', 'G'}},
		{MimeType: "application/pdf", Filename: "sample.pdf", Data: []byte("%PDF")},
	}
	require.NoError(t, a.SendChildInput(steerChildID, "hello", attachments))
	params := lastRequest(t, childStdin)
	text, _ := params["text"].(string)
	assert.Contains(t, text, "hello")
	assert.Contains(t, text, "notes")
	assert.NotContains(t, text, "[image: shot.png]", "the model receives image bytes separately")
	assert.Equal(t, []any{map[string]any{
		"type": "base64", "mediaType": "image/png", "data": "iVBORw==",
	}}, params["images"])
	assert.Equal(t, []any{map[string]any{
		"type": "base64", "mediaType": "application/pdf", "data": "JVBERg==", "name": "sample.pdf",
	}}, params["files"])
	assert.Empty(t, rootStdin.frames(), "the parent process must not receive the child's attachment")
}
