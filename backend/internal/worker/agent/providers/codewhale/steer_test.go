package codewhale

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// steerRoute is the steer route of the test turn.
var steerRoute = turnPath(testThreadID, testTurnID, turnRouteSteer)

// newSteeringAgent builds an agent whose turn testTurnID runs. Its sink records
// each steer that the agent hands back to the queue (RequeuedInputs).
func newSteeringAgent(t *testing.T, rt *fakeRuntime) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	a, sink := newTestAgentWith(t, rt, testAgentOptions{})
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.store = codewhaleStore{dir: t.TempDir()}
	return a, sink
}

// steeredEvent is the runtime's `turn.steered` for input.
func steeredEvent(seq uint64, input string) []byte {
	return runtimeEvent(seq, "turn.steered", testTurnID, "item_steer", map[string]any{"thread_id": testThreadID, "turn_id": testTurnID, "input": input})
}

// steerDroppedEvent is the runtime's `turn.steer_dropped` for input.
func steerDroppedEvent(seq uint64, input string) []byte {
	return runtimeEvent(seq, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer", map[string]any{
		"thread_id": testThreadID, "turn_id": testTurnID, "input": input,
		"reason": "the turn moved on before the engine committed it, so the model never saw it — resend it",
		"item":   map[string]any{"id": "item_steer", "kind": "user_message", "status": "canceled", "detail": input},
	})
}

// acceptSteers makes the route answer each steer with 200.
func acceptSteers(rt *fakeRuntime) {
	rt.respondJSON(http.MethodPost, steerRoute, http.StatusOK, map[string]any{"id": testTurnID, "status": "in_progress"})
}

// writeTurnRecord writes the test turn's record into the agent's store, with one
// user_message item for each status and text pair.
func writeTurnRecord(t *testing.T, a *Agent, items ...[2]string) {
	t.Helper()
	root := a.store.runtimeDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "turns"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "items"), 0o700))
	ids := make([]string, 0, len(items)+1)
	// A tool item of the same turn, which the reader skips.
	require.NoError(t, os.WriteFile(filepath.Join(root, "items", "item_tool.json"), mustJSON(t, map[string]any{"id": "item_tool", "kind": "tool_call", "status": "completed", "detail": "Also this."}), 0o600))
	ids = append(ids, "item_tool")
	for i, item := range items {
		id := "item_user_" + string(rune('a'+i))
		require.NoError(t, os.WriteFile(filepath.Join(root, "items", id+".json"), mustJSON(t, map[string]any{"id": id, "kind": "user_message", "status": item[0], "detail": item[1]}), 0o600))
		ids = append(ids, id)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "turns", testTurnID+".json"), mustJSON(t, map[string]any{"id": testTurnID, "item_ids": ids}), 0o600))
}

func openSteers(a *Agent) int {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return len(a.steers.open)
}

func TestSteerInputReadsARefusalAsNoTurn(t *testing.T) {
	t.Parallel()
	// 409: the engine dropped the steer within the route's wait, or no turn
	// runs. 404: the thread is gone. 400: the turn is stopping, or it is no
	// longer in progress.
	for _, status := range []int{http.StatusConflict, http.StatusNotFound, http.StatusBadRequest} {
		rt := newFakeRuntime(t)
		rt.respondStatus(http.MethodPost, steerRoute, status, "Turn "+testTurnID+" is stopping and cannot be steered")
		a, _ := newSteeringAgent(t, rt)
		assert.ErrorIs(t, a.SteerInput("More.", nil), agent.ErrNoActiveTurn, status)
		assert.Zero(t, openSteers(a), status)
	}
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, steerRoute, http.StatusInternalServerError, "Failed to persist steer")
	a, sink := newSteeringAgent(t, rt)
	err := a.SteerInput("More.", nil)
	assert.ErrorContains(t, err, "Failed to persist steer")
	assert.NotErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Zero(t, openSteers(a))
	// The queue does not send a failed steer again, so the agent does not absorb
	// a drop of it: the runtime's event states it to the reader.
	a.HandleOutput(steerDroppedEvent(2, "More."))
	assert.Equal(t, 1, sink.NotificationCount())
}

// The verdict can reach the stream before the connection drops. The verdict
// then settles the steer, and the drop is no uncertain delivery.
func TestALostReplyAfterTheVerdictSettlesTheSteer(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		event func(uint64, string) []byte
		want  error
	}{
		"a delivery": {steeredEvent, nil},
		"a drop":     {steerDroppedEvent, agent.ErrNoActiveTurn},
	} {
		rt := newFakeRuntime(t)
		a, sink := newSteeringAgent(t, rt)
		rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, _ *http.Request) {
			a.HandleOutput(tc.event(2, "Also this."))
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hijacker.Hijack()
			require.NoError(t, err)
			_ = conn.Close()
		})

		err := a.SteerInput("Also this.", nil)
		if tc.want == nil {
			assert.NoError(t, err, name)
		} else {
			assert.ErrorIs(t, err, tc.want, name)
		}
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, name)
		assert.Zero(t, openSteers(a), name)
		assert.Zero(t, sink.NotificationCount(), name)
		assert.Empty(t, sink.RequeuedInputs(), name)
	}
}

// A steer event that states no turn identifies no steer. A drop of it states
// itself, and a delivery of it settles nothing.
func TestASteerEventThatStatesNoTurn(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	require.NoError(t, a.SteerInput("Also this.", nil))

	a.HandleOutput([]byte(`{"seq":2,"event":"turn.steered","thread_id":"` + testThreadID + `","payload":{"input":"Also this."}}`))
	assert.Equal(t, 1, openSteers(a), "the delivery addresses no turn")
	a.HandleOutput([]byte(`{"seq":3,"event":"turn.steer_dropped","thread_id":"` + testThreadID + `","payload":{"input":"Also this."}}`))
	assert.Equal(t, 1, sink.NotificationCount(), "the drop states itself")
	assert.Empty(t, sink.RequeuedInputs())
	assert.Equal(t, 1, openSteers(a))
}

func TestSteerInputReadsALostReplyAsUncertain(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, _ *http.Request) {
		// The connection drops after the engine may have taken the text.
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	})
	a, _ := newSteeringAgent(t, rt)
	assert.ErrorIs(t, a.SteerInput("More.", nil), agent.ErrDeliveryUncertain)
	assert.Zero(t, openSteers(a))
}

// The queue sends a refused steer as a turn of its own. The runtime states the
// drop as well, and the runtime's reason -- "resend it" -- must not reach the
// reader for a message that the queue sends again.
func TestTheDropOfARefusedSteerDrawsNoRow(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, steerRoute, http.StatusConflict, "Turn moved on before the steer reached the model")
	a, sink := newSteeringAgent(t, rt)

	require.ErrorIs(t, a.SteerInput("Also this.", nil), agent.ErrNoActiveTurn)
	a.HandleOutput(steerDroppedEvent(2, "Also this."))
	assert.Zero(t, sink.NotificationCount())
	assert.Empty(t, sink.RequeuedInputs(), "the queue sends it already")
}

// The drop can reach the stream before the route's answer reaches the agent.
// The queue has not counted the steer yet, so the agent refuses it.
func TestADropBeforeTheAnswerRefusesTheSteer(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newSteeringAgent(t, rt)
	rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, _ *http.Request) {
		a.HandleOutput(steerDroppedEvent(2, "Also this."))
		writeFakeJSON(w, http.StatusOK, map[string]any{"id": testTurnID})
	})

	assert.ErrorIs(t, a.SteerInput("Also this.", nil), agent.ErrNoActiveTurn)
	assert.Zero(t, sink.NotificationCount())
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, openSteers(a))
}

func TestADeliveryBeforeTheAnswerSettlesTheSteer(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := newSteeringAgent(t, rt)
	rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, _ *http.Request) {
		a.HandleOutput(steeredEvent(2, "Also this."))
		writeFakeJSON(w, http.StatusOK, map[string]any{"id": testTurnID})
	})

	require.NoError(t, a.SteerInput("Also this.", nil))
	assert.Zero(t, openSteers(a))
}

// The route answered 200 with the text still queued, and the queue counts it
// delivered. The runtime drops it later: the text goes back to the queue.
func TestADropAfterTheAnswerHandsTheSteerBack(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	attachments := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("alpha")}}

	require.NoError(t, a.SteerInput("Also this.", attachments))
	prompt := rt.lastBody(t, http.MethodPost, steerRoute)["prompt"].(string)
	require.Contains(t, prompt, "alpha", "the route takes the attachment inline")
	assert.Equal(t, 1, openSteers(a))

	a.HandleOutput(steerDroppedEvent(2, prompt))
	steers := sink.RequeuedInputs()
	require.Len(t, steers, 1)
	assert.Equal(t, "Also this.", steers[0].Content, "the queue takes the message as the reader sent it")
	assert.Equal(t, attachments, steers[0].Attachments)
	assert.Equal(t, testTurnID+"/1", steers[0].DropID)
	assert.Zero(t, sink.NotificationCount(), "the queue sends it again, so the reader is not told to")
	assert.Zero(t, openSteers(a))

	// A replay of the same drop finds nothing to hand back.
	a.HandleOutput(steerDroppedEvent(3, prompt))
	assert.Len(t, sink.RequeuedInputs(), 1)
}

// When the queue refuses the text, nothing sends it again, and the runtime's
// event states the drop to the reader.
func TestADropThatTheQueueRefusesIsStated(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	sink.RequeueErr = errors.New("the queue is full")

	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steerDroppedEvent(2, "Also this."))
	require.Equal(t, 1, sink.NotificationCount())
	assert.Equal(t, contracts.CodewhaleEventTurnSteerDropped, decodeJSON(t, sink.LastNotification().Content)["event"])
	assert.Zero(t, openSteers(a))
}

// When the queue refuses the text at the turn end, the ticket stays, so the
// runtime's later drop event states the drop to the reader.
func TestATurnEndHandBackThatTheQueueRefusesIsStatedByTheDropEvent(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	sink.RequeueErr = errors.New("the queue is full")

	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusQueued, "Also this."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	assert.Equal(t, 1, openSteers(a), "the ticket waits for the drop event")
	assert.Zero(t, sink.NotificationCount())

	a.HandleOutput(steerDroppedEvent(3, "Also this."))
	assert.Equal(t, 1, sink.NotificationCount())
	assert.Zero(t, openSteers(a))
}

// A drop of a steer that LeapMux did not send states itself too.
func TestAnUnknownDropIsStated(t *testing.T) {
	t.Parallel()
	a, sink := newSteeringAgent(t, nil)
	a.HandleOutput(steerDroppedEvent(2, "Something else."))
	assert.Equal(t, 1, sink.NotificationCount())
	assert.Empty(t, sink.RequeuedInputs())
}

func TestADeliveredSteerIsForgotten(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steeredEvent(2, "Also this."))
	assert.Zero(t, openSteers(a))
	writeTurnRecord(t, a, [2]string{itemStatusQueued, "Also this."})
	a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Empty(t, sink.RequeuedInputs(), "the model read it")
}

// A steer that waits in the engine's mailbox when its turn ends is dropped only
// when the next turn starts. The turn's record states it at once.
func TestASteerThatItsTurnNeverCommittedIsHandedBackAtTheTurnEnd(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusCompleted, "Do it."}, [2]string{itemStatusQueued, "Also this."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))

	steers := sink.RequeuedInputs()
	require.Len(t, steers, 1)
	assert.Equal(t, "Also this.", steers[0].Content)
	assert.True(t, steers[0].TurnActive, "the text is back in the queue before the queue learns that the turn ended")
	lifecycle := sink.TurnLifecycle()
	requeued := slices.Index(lifecycle, "requeue:"+testTurnID+"/1")
	ended := slices.Index(lifecycle, "turn_active:false")
	require.GreaterOrEqual(t, requeued, 0, lifecycle)
	assert.Less(t, requeued, ended, lifecycle)
	last, _ := sink.LastTurnActive()
	assert.False(t, last)
	assert.Zero(t, openSteers(a))

	// The runtime states the drop when the next turn starts. The queue sends the
	// text already, so that event draws no row.
	a.HandleOutput(turnStartedEvent(3, "turn_next"))
	a.HandleOutput(runtimeEvent(4, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer", map[string]any{"turn_id": testTurnID, "input": "Also this."}))
	assert.Zero(t, sink.NotificationCount())
	assert.Len(t, sink.RequeuedInputs(), 1)
}

// Two steers with one text: the record decides how many the model read.
func TestTheTurnEndCountsSteersOfOneText(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Again.", nil))
	require.NoError(t, a.SteerInput("Again.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusCompleted, "Again."}, [2]string{itemStatusCanceled, "Again."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusFailed))

	assert.Len(t, sink.RequeuedInputs(), 1)
	assert.Zero(t, openSteers(a))
}

// A turn record that cannot be read proves nothing, so the steer waits for its
// event.
func TestASteerThatTheRecordDoesNotStateWaitsForItsEvent(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	assert.Empty(t, sink.RequeuedInputs())
	assert.Equal(t, 1, openSteers(a))

	a.HandleOutput(steerDroppedEvent(3, "Also this."))
	assert.Len(t, sink.RequeuedInputs(), 1)
}

// Nothing states a steer after the process exits, so the exit reads the record
// for every steer that waits.
func TestAnExitHandsBackTheSteersThatTheRecordStatesUndelivered(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	rt.respondJSON(http.MethodPost, turnPath(testThreadID, testTurnID, turnRouteInterrupt), http.StatusOK, map[string]any{"id": testTurnID})
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	require.NoError(t, a.SteerInput("And this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusQueued, "Also this."})
	a.Stop()

	steers := sink.RequeuedInputs()
	require.Len(t, steers, 1, "a steer that the record does not state is not sent twice")
	assert.Equal(t, "Also this.", steers[0].Content)
	assert.Zero(t, openSteers(a))
}

// A process whose output is discarded -- a context clear -- hands nothing back:
// the steer belongs to the context that the reader cleared.
func TestADiscardedProcessHandsNothingBack(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	rt.respondJSON(http.MethodPost, turnPath(testThreadID, testTurnID, turnRouteInterrupt), http.StatusOK, map[string]any{"id": testTurnID})
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusQueued, "Also this."})
	a.DiscardOutput()
	a.Stop()
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, openSteers(a))
}

func TestReadTurnSteersRefusesAPathOutsideTheStore(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	for _, turnID := range []string{"", "../turn", `turns\x`} {
		_, _, ok := readTurnSteers(store, turnID)
		assert.False(t, ok, turnID)
	}
	_, _, ok := readTurnSteers(codewhaleStore{}, testTurnID)
	assert.False(t, ok, "an agent with no store reads nothing")
}

func TestSteerAbsorbRingForgetsTheOldest(t *testing.T) {
	t.Parallel()
	var steers codewhaleSteers
	first := steerKey{turnID: "t", input: "first"}
	steers.absorb(first)
	for i := range steerAbsorbCapacity {
		steers.absorb(steerKey{turnID: "t", input: string(rune('a' + i%26))})
	}
	assert.Len(t, steers.absorbed, steerAbsorbCapacity)
	assert.False(t, steers.takeAbsorbed(first), "the oldest drop is forgotten")
	assert.True(t, steers.takeAbsorbed(steerKey{turnID: "t", input: "a"}))
	assert.False(t, steers.takeAbsorbed(steerKey{turnID: "other", input: "a"}), "a drop of another turn is another drop")
}
