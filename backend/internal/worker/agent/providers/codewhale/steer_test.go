package codewhale

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// steerRoute is the steer route of the test turn.
var steerRoute = turnPath(testThreadID, testTurnID, turnRouteSteer)

// newSteeringAgent starts one native turn and records shared steer outcome reports.
func newSteeringAgent(t *testing.T, rt *fakeRuntime, options ...testAgentOptions) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	opts := testAgentOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	a, sink := newTestAgentWith(t, rt, opts)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.store = codewhaleStore{dir: t.TempDir()}
	writeTurnRecord(t, a)
	if rt != nil {
		rt.mu.Lock()
		rt.steerStore = a.store
		rt.mu.Unlock()
	}
	return a, sink
}

// steeredEvent is the runtime's `turn.steered` for input.
func steeredEvent(seq uint64, input string) []byte {
	return runtimeEvent(seq, "turn.steered", testTurnID, "item_steer_1", map[string]any{"thread_id": testThreadID, "turn_id": testTurnID, "input": input})
}

// steerDroppedEvent is the runtime's `turn.steer_dropped` for input.
func steerDroppedEvent(seq uint64, input string) []byte {
	return runtimeEvent(seq, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer_1", map[string]any{
		"thread_id": testThreadID, "turn_id": testTurnID, "input": input,
		"reason": "the turn moved on before the engine committed it, so the model never saw it — resend it",
		"item":   map[string]any{"id": "item_steer_1", "kind": "user_message", "status": "canceled", "detail": input},
	})
}

// acceptSteers persists the queued native item before its HTTP acknowledgment, as the runtime does.
func acceptSteers(rt *fakeRuntime) {
	rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, request *http.Request) {
		var input struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rt.mu.Lock()
		store := rt.steerStore
		rt.steerSequence++
		id := "item_steer_" + strconv.FormatUint(rt.steerSequence, 10)
		rt.mu.Unlock()
		root := store.runtimeDir()
		data, err := os.ReadFile(filepath.Join(root, "turns", testTurnID+".json"))
		var turn storedTurn
		if err == nil {
			err = json.Unmarshal(data, &turn)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		item := storedItem{ID: id, TurnID: testTurnID, Kind: itemKindUserMessage, Status: itemStatusQueued, Detail: strings.TrimSpace(input.Prompt)}
		data, err = json.Marshal(item)
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "items", id+".json"), data, 0o600)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		turn.ItemIDs = append(turn.ItemIDs, id)
		data, err = json.Marshal(turn)
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "turns", testTurnID+".json"), data, 0o600)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFakeJSON(w, http.StatusOK, turn)
	})
}

var fixtureSteerSequence atomic.Uint64

// writeTurnRecord changes native statuses without changing the identities of existing matching inputs.
func writeTurnRecord(t *testing.T, a *Agent, items ...[2]string) {
	t.Helper()
	root := a.store.runtimeDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "turns"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "items"), 0o700))
	var previous storedTurn
	if data, err := os.ReadFile(filepath.Join(root, "turns", testTurnID+".json")); err == nil {
		require.NoError(t, json.Unmarshal(data, &previous))
	} else {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	var existing []storedItem
	for _, id := range previous.ItemIDs {
		if item, ok := readSteerItem(a.store, testTurnID, id); ok {
			existing = append(existing, item)
		}
	}
	ids := []string{"item_tool"}
	require.NoError(t, os.WriteFile(filepath.Join(root, "items", "item_tool.json"), mustJSON(t, map[string]any{"id": "item_tool", "turn_id": testTurnID, "kind": "tool_call", "status": "completed", "detail": "Also this."}), 0o600))
	used := make(map[string]bool)
	for _, entry := range items {
		id := ""
		for _, old := range existing {
			if !used[old.ID] && strings.TrimSpace(old.Detail) == strings.TrimSpace(entry[1]) {
				id = old.ID
				break
			}
		}
		if id == "" {
			id = "item_fixture_" + strconv.FormatUint(fixtureSteerSequence.Add(1), 10)
		}
		used[id] = true
		require.NoError(t, os.WriteFile(filepath.Join(root, "items", id+".json"), mustJSON(t, storedItem{ID: id, TurnID: testTurnID, Kind: itemKindUserMessage, Status: entry[0], Detail: entry[1]}), 0o600))
		ids = append(ids, id)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "turns", testTurnID+".json"), mustJSON(t, storedTurn{ID: testTurnID, ItemIDs: ids}), 0o600))
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
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs(), "the original input and attachments remain in the shared queue")
	assert.Zero(t, sink.NotificationCount(), "the queue sends it again, so the reader is not told to")
	assert.Zero(t, openSteers(a))

	// A replay of the same drop finds nothing to hand back.
	a.HandleOutput(steerDroppedEvent(3, prompt))
	assert.Len(t, sink.SteerEchoReports(), 1)
	assert.Zero(t, sink.NotificationCount())
}

// Returning a held input changes its state. It does not insert a second input into a full queue.
func TestADropUsesTheHeldInputWhenANewQueueInsertionWouldFail(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	sink.RequeueErr = errors.New("the queue is full")
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steerDroppedEvent(2, "Also this."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, openSteers(a))
}

func TestATurnEndUsesTheHeldInputWhenANewQueueInsertionWouldFail(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	sink.RequeueErr = errors.New("the queue is full")
	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusCanceled, "Also this."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, openSteers(a))
	a.HandleOutput(steerDroppedEvent(3, "Also this."))
	assert.Zero(t, sink.NotificationCount())
	assert.Len(t, sink.SteerEchoReports(), 1)
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

// A canceled native item proves that the model did not read the steer.
func TestASteerThatItsTurnNeverCommittedIsHandedBackAtTheTurnEnd(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusCompleted, "Do it."}, [2]string{itemStatusCanceled, "Also this."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))

	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
	lifecycle := sink.TurnLifecycle()
	requeued := slices.Index(lifecycle, "steer-dropped:1")
	ended := slices.Index(lifecycle, "turn_active:false")
	require.GreaterOrEqual(t, requeued, 0, lifecycle)
	assert.Less(t, requeued, ended, lifecycle)
	last, _ := sink.LastTurnActive()
	assert.False(t, last)
	assert.Zero(t, openSteers(a))

	// The runtime states the drop when the next turn starts. The queue sends the
	// text already, so that event draws no row.
	a.HandleOutput(turnStartedEvent(3, "turn_next"))
	a.HandleOutput(runtimeEvent(4, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer_1", map[string]any{"turn_id": testTurnID, "input": "Also this."}))
	assert.Zero(t, sink.NotificationCount())
	assert.Len(t, sink.SteerEchoReports(), 1)
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

	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, openSteers(a))
}

// An unproved delivery remains in the queue as uncertain. No input is replayed automatically.
func TestASteerThatTheRecordDoesNotProveBecomesUncertain(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)

	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	assert.Empty(t, sink.RequeuedInputs())
	assert.Zero(t, openSteers(a))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())

	a.HandleOutput(steerDroppedEvent(3, "Also this."))
	assert.Empty(t, sink.RequeuedInputs())
	assert.Len(t, sink.SteerEchoReports(), 1)
	assert.Zero(t, sink.NotificationCount())
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
	writeTurnRecord(t, a, [2]string{itemStatusCanceled, "Also this."})
	a.Stop()

	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}, {Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs(), "an unproved steer must not be sent twice")
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
		_, ok := readTurnSteers(store, turnID, nil)
		assert.False(t, ok, turnID)
	}
	_, ok := readTurnSteers(codewhaleStore{}, testTurnID, nil)
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

func TestCodewhaleSteerUsesTheSharedEchoPolicy(t *testing.T) {
	t.Parallel()
	a, _ := newSteeringAgent(t, nil)
	echo, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok)
	assert.True(t, echo.SteerRowAtEcho())
	survivor, ok := any(a).(agent.SteerSurvivor)
	require.True(t, ok)
	assert.True(t, survivor.SteerSurvivesTurnEnd())
}

func TestCodewhaleSteerRowWaitsForTheNativeEcho(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a)
	require.NoError(t, a.SteerInput("Also this.", nil))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "The preceding answer.", nil))
	require.Len(t, sink.Messages(), 1)
	a.HandleOutput(steeredEvent(3, "Also this."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
}

func TestCodewhaleSteerDropReturnsTheHeldInput(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a)
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steerDroppedEvent(2, "Also this."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs(), "the shared queue already owns the input")
}

func TestCodewhaleSteerReportsFollowSendOrder(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a)
	require.NoError(t, a.SteerInput("First.", nil))
	require.NoError(t, a.SteerInput("Second.", nil))
	a.HandleOutput(runtimeEvent(2, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer_2", map[string]any{"turn_id": testTurnID, "input": "Second."}))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(steeredEvent(3, "First."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleRepeatedReceiptCannotResolveAnIdenticalInput(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	require.NoError(t, a.SteerInput("Again.", nil))
	require.NoError(t, a.SteerInput("Again.", nil))
	a.HandleOutput(steeredEvent(2, "Again."))
	a.HandleOutput(steeredEvent(3, "Again."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	assert.Equal(t, 1, openSteers(a))
	a.HandleOutput(runtimeEvent(4, contracts.CodewhaleEventTurnSteerDropped, testTurnID, "item_steer_2", map[string]any{"turn_id": testTurnID, "input": "Again."}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleUnreadableSteerRecordProvesNoOutcome(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "{", `{"id":"another","turn_id":"turn-1","kind":"user_message","status":"completed","detail":"Again."}`} {
		t.Run(content, func(t *testing.T) {
			rt := newFakeRuntime(t)
			acceptSteers(rt)
			a, sink := newSteeringAgent(t, rt)
			require.NoError(t, a.SteerInput("Again.", nil))
			path := filepath.Join(a.store.runtimeDir(), "items", "item_steer_1.json")
			if content == "" {
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
			assert.Empty(t, sink.RequeuedInputs())
		})
	}
}

func TestCodewhaleDiscardDropsAHeldEcho(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steeredEvent(3, "Also this."))
	require.Empty(t, sink.SteerEchoReports())
	a.DiscardOutput()
	a.finishAfterExit(agent.MessageCompletionInterrupted)
	assert.Empty(t, sink.SteerEchoReports())
	assert.Zero(t, openSteers(a))
}

func TestCodewhaleOnlyTheCurrentSteerCallReleasesTheTurnEnd(t *testing.T) {
	t.Parallel()
	a, sink := newSteeringAgent(t, nil)
	current := a.steers.begin(testTurnID, "Current.", nil, false)
	a.steerCall = current
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	a.finishSteerCall(&steerTicket{})
	assert.Empty(t, sink.Messages())
	assert.NotNil(t, a.pendingTurnEnd)
	a.finishSteerCall(current)
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
}

func TestCodewhaleSteerRecoveryRejectsAnOlderIdenticalMessage(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a, [2]string{itemStatusCompleted, "Again."})
	require.NoError(t, a.SteerInput("Again.", nil))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Zero(t, openSteers(a))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports(), "an old message cannot prove the new steer was read")
	a.finishAfterExit(agent.MessageCompletionComplete)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleQueuedRecordDoesNotProveARejection(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a)
	require.NoError(t, a.SteerInput("Also this.", nil))
	writeTurnRecord(t, a, [2]string{itemStatusQueued, "Also this."})
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Zero(t, openSteers(a))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.RequeuedInputs())
	a.HandleOutput(steeredEvent(3, "Also this."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleTurnEndWaitsForThePendingSteerReply(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newSteeringAgent(t, rt)
	writeTurnRecord(t, a)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	rt.handle(http.MethodPost, steerRoute, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"id": testTurnID, "status": "in_progress"})
	})
	result := make(chan error, 1)
	go func() { result <- a.SteerInput("Also this.", nil) }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the steer did not reach the native route")
	}
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	a.HandleOutput(turnStartedEvent(3, "next-turn"))
	a.HandleOutput(runtimeEvent(4, contracts.CodewhaleEventItemCompleted, "next-turn", "next-item", map[string]any{"item": map[string]any{"id": "next-item", "turn_id": "next-turn", "kind": contracts.CodewhaleItemKindAgentMessage, "detail": "The next answer."}}))
	assert.Empty(t, sink.Messages(), "the final row and later output wait for the steer request")
	a.HandleOutput(steeredEvent(5, "Also this."))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	unblock()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the steer did not finish")
	}
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[0].TurnEnd)
	assert.Contains(t, string(rows[1].Content), "The next answer.")
}

func TestCodewhaleEchoWaitsForThePrecedingGeneratedItem(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	// The native trace reports turn.steered before the preceding item.completed.
	// Source: .tmp/probe/codewhale/logs/runtime-events.jsonl, frames 25-42.
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "HEAD "}))
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(runtimeEvent(4, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "TAIL"}))
	a.HandleOutput(steeredEvent(5, "Also this."))
	assert.Empty(t, sink.SteerEchoReports())
	assert.Empty(t, sink.Messages(), "the echo must not split the native answer")
	final := itemEvent(6, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "HEAD TAIL", nil)
	a.HandleOutput(final)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, final, rows[0].Content)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleEchoWaitsForThePrecedingToolResult(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	a.HandleOutput(toolStartEvent(2, "tool-item", "tool-call", contracts.CodewhaleToolBash, map[string]any{"command": "printf done"}))
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steeredEvent(3, "Also this."))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(toolEndEvent(4, contracts.CodewhaleEventItemCompleted, "tool-item", "tool-call", contracts.CodewhaleToolBash, "done", map[string]any{"command": "printf done"}, nil))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleEchoWaitsForAFailedPrecedingWrite(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	var blocked *blockedCodewhaleMessages
	a, sink := newSteeringAgent(t, rt, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
		blocked = &blockedCodewhaleMessages{ProviderServices: inner}
		return blocked
	}})
	a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steeredEvent(3, "Also this."))
	blocked.blocked.Store(true)
	final := itemEvent(4, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "The complete answer.", nil)
	a.HandleOutput(final)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.SteerEchoReports())
	blocked.blocked.Store(false)
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, final, rows[0].Content)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhaleEchoReleasesAfterAnAbandonedPredecessor(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{contracts.CodewhaleItemKindAgentMessage, contracts.CodewhaleItemKindAgentReasoning} {
		t.Run(kind, func(t *testing.T) {
			rt := newFakeRuntime(t)
			acceptSteers(rt)
			a, sink := newSteeringAgent(t, rt)
			a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "old", kind, "", nil))
			a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "old", map[string]any{"kind": kind, "delta": "DISCARDED-ATTEMPT"}))
			require.NoError(t, a.SteerInput("Also this.", nil))
			a.HandleOutput(steeredEvent(4, "Also this."))
			assert.Empty(t, sink.SteerEchoReports())
			if kind == contracts.CodewhaleItemKindAgentReasoning {
				a.HandleOutput(requestSnapshotEvent(5, 1, false))
			}
			a.HandleOutput(itemEvent(6, contracts.CodewhaleEventItemStarted, "new", contracts.CodewhaleItemKindAgentMessage, "", nil))
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
			a.HandleOutput(itemEvent(7, contracts.CodewhaleEventItemCompleted, "new", contracts.CodewhaleItemKindAgentMessage, "The new answer.", nil))
			a.HandleOutput(turnCompletedEvent(8, testTurnID, contracts.CodewhaleTurnStatusCompleted))
			for _, row := range sink.Messages() {
				assert.NotContains(t, string(row.Content), "DISCARDED-")
			}
		})
	}
}

func TestCodewhaleEchoWaitsThroughARepeatedOrInvalidStart(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, id, kind string }{
		{"repeated", "answer", contracts.CodewhaleItemKindAgentMessage},
		{"absent identity", "", contracts.CodewhaleItemKindAgentMessage},
		{"conflicting kind", "answer", contracts.CodewhaleItemKindAgentReasoning},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := newFakeRuntime(t)
			acceptSteers(rt)
			a, sink := newSteeringAgent(t, rt)
			a.HandleOutput(itemEvent(2, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
			a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The preceding answer."}))
			require.NoError(t, a.SteerInput("Also this.", nil))
			a.HandleOutput(steeredEvent(4, "Also this."))
			a.HandleOutput(itemEvent(5, contracts.CodewhaleEventItemStarted, test.id, test.kind, "", nil))
			assert.Empty(t, sink.SteerEchoReports())
			_, text, present := a.generationBuffer.ScopeSnapshot("answer")
			assert.True(t, present)
			assert.Equal(t, "The preceding answer.", text)
			a.HandleOutput(itemEvent(6, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "The preceding answer.", nil))
			assert.Len(t, sink.Messages(), 1)
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
		})
	}
}

func requestSnapshotEvent(seq uint64, step int, finished bool) []byte {
	snapshot := map[string]any{"turn_id": map[string]any{"value": "engine-turn", "truncated": false}, "step": step}
	if finished {
		snapshot["terminal"] = map[string]any{"status": "completed"}
	}
	return runtimeEvent(seq, eventModelToolsSnapshot, testTurnID, "", map[string]any{"snapshot": snapshot})
}

func TestCodewhaleEchoWaitsForTheCompleteModelRequest(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	a.HandleOutput(requestSnapshotEvent(2, 0, false))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemStarted, "thought", contracts.CodewhaleItemKindAgentReasoning, "", nil))
	require.NoError(t, a.SteerInput("Also this.", nil))
	a.HandleOutput(steeredEvent(4, "Also this."))
	a.HandleOutput(itemEvent(5, contracts.CodewhaleEventItemCompleted, "thought", contracts.CodewhaleItemKindAgentReasoning, "The preceding thought.", nil))
	assert.Empty(t, sink.SteerEchoReports(), "the same request can still produce text")
	a.HandleOutput(itemEvent(6, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(itemEvent(7, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "The preceding answer.", nil))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(requestSnapshotEvent(8, 1, false))
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodewhalePreparedRequestDiscardsAbandonedGeneratedKinds(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(requestSnapshotEvent(2, 0, false))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemStarted, "thought", contracts.CodewhaleItemKindAgentReasoning, "", nil))
	a.HandleOutput(runtimeEvent(4, eventItemDelta, testTurnID, "thought", map[string]any{"kind": contracts.CodewhaleItemKindAgentReasoning, "delta": "DISCARDED-THOUGHT"}))
	a.HandleOutput(itemEvent(5, contracts.CodewhaleEventItemStarted, "answer", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(6, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "DISCARDED-ANSWER"}))
	// A stream resume prepares another request at the same model-step index.
	// Source: .tmp/probe/codewhale/logs/runtime-events.jsonl, frames 6 and 12.
	a.HandleOutput(requestSnapshotEvent(7, 0, false))
	a.HandleOutput(itemEvent(8, contracts.CodewhaleEventItemCompleted, "next", contracts.CodewhaleItemKindAgentMessage, "The clean answer.", nil))
	a.HandleOutput(turnCompletedEvent(9, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.NotContains(t, string(row.Content), "DISCARDED-")
	}
}

func TestCodewhaleFinalRequestSnapshotKeepsInterruptedOutput(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(requestSnapshotEvent(2, 0, false))
	a.HandleOutput(runtimeEvent(3, eventItemDelta, testTurnID, "answer", map[string]any{"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "The partial answer."}))
	a.HandleOutput(requestSnapshotEvent(4, 0, true))
	a.HandleOutput(turnCompletedEvent(5, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "The partial answer.", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
}
