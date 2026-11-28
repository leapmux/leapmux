package inputqueue

import (
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentifiedSteerEchoCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	f := newEchoQueueFixture(t, true)
	f.enqueue(t, "steer")
	f.dispatcher.steerFail = errors.New("the acknowledgement was lost after the native echo")
	receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "native-input", TurnID: "receiving-turn"}
	f.dispatcher.onSteer = func(item DispatchItem) {
		require.NotEmpty(t, item.SteerAttemptID)
		report := agent.SteerResolution{
			InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: receipt,
		}
		matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.True(t, matched)
		matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.False(t, matched, "a repeated echo writes no second row")
	}
	snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	require.Len(t, f.observer.accepted, 1)
	assert.Equal(t, "steer", f.observer.accepted[0].ID)
	var session, nativeID, turn string
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `
		SELECT m.agent_session_id, m.native_input_id, binding.native_turn_id
		FROM messages m JOIN native_agent_inputs binding USING (agent_id, agent_session_id, native_input_id)
		WHERE m.id = 'steer'`).Scan(&session, &nativeID, &turn))
	assert.Equal(t, receipt, agent.NativeInputReceipt{SessionID: session, InputID: nativeID, TurnID: turn})
}

func TestIdentifiedSteerDropCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	f := newEchoQueueFixture(t, true)
	f.enqueue(t, "steer")
	f.dispatcher.onSteer = func(item DispatchItem) {
		report := agent.SteerResolution{
			InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeDropped,
		}
		matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.True(t, matched)
		matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
		require.NoError(t, err)
		assert.False(t, matched)
	}
	snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "steer", snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	assert.Empty(t, f.observer.accepted)
}

func TestIdentifiedSteerUncertaintyCanPrecedeDispatchReturn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		paused bool
		reason leapmuxv1.AgentInputQueuePauseReason
	}{
		{"open queue", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN},
		{"manual pause", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoQueueFixture(t, true)
			_, err := f.manager.SetPaused(t.Context(), "agent-1", tc.paused)
			require.NoError(t, err)
			f.enqueue(t, "steer")
			f.enqueue(t, "next")
			f.dispatcher.onSteer = func(item DispatchItem) {
				report := agent.SteerResolution{
					InputID: item.ID, AttemptID: item.SteerAttemptID, Outcome: agent.SteerOutcomeUncertain,
				}
				matched, err := f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
				require.NoError(t, err)
				assert.True(t, matched)
				matched, err = f.manager.ResolveSteer(t.Context(), item.AgentID, report, "[]")
				require.NoError(t, err)
				assert.False(t, matched)
			}
			snapshot, err := f.manager.Steer(t.Context(), "agent-1", "steer")
			require.NoError(t, err)
			assert.Equal(t, tc.reason, snapshot.PauseReason)
			assert.False(t, snapshot.ActiveTurn)
			require.Len(t, snapshot.Items, 2)
			assert.Equal(t, "steer", snapshot.Items[0].ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
			assert.Equal(t, "next", snapshot.Items[1].ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[1].State)
			f.observer.mu.Lock()
			defer f.observer.mu.Unlock()
			assert.Empty(t, f.observer.accepted)
		})
	}
}

func TestIdentifiedSteerDropPreservesInputAndRejectsPreviousAttempt(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	holdEchoSteer(t, store, "older")
	first := prepareEchoSteer(t, store, "steer")
	_, err := store.HoldForEcho(ctx, first)
	require.NoError(t, err)
	drop := agent.SteerResolution{
		InputID: first.Item.ID, AttemptID: first.Item.SteerAttemptID, Outcome: agent.SteerOutcomeDropped,
	}
	result, err := store.resolveSteer(ctx, "agent-1", drop, "", "")
	require.NoError(t, err)
	require.True(t, result.matched)
	require.Len(t, result.snapshot.Items, 2)
	assert.Equal(t, "older", result.snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, result.snapshot.Items[0].State)
	assert.Equal(t, first.Item.ID, result.snapshot.Items[1].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, result.snapshot.Items[1].State)
	next, _, err := store.PrepareSteer(ctx, "agent-1", "steer", true)
	require.NoError(t, err)
	require.NotEqual(t, first.Item.SteerAttemptID, next.Item.SteerAttemptID)
	_, err = store.HoldForEcho(ctx, *next)
	require.NoError(t, err)
	echo := agent.SteerResolution{
		InputID: first.Item.ID, AttemptID: first.Item.SteerAttemptID, Outcome: agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "second-native-input", TurnID: "second-turn"},
	}
	for _, stale := range []agent.SteerResolution{drop, echo} {
		result, err = store.resolveSteer(ctx, "agent-1", stale, "", "")
		require.NoError(t, err)
		assert.False(t, result.matched)
		assert.Empty(t, result.accepted)
		snapshot, err := store.Snapshot(ctx, "agent-1")
		require.NoError(t, err)
		require.Len(t, snapshot.Items, 2)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, snapshot.Items[1].State)
	}
	echo.AttemptID = next.Item.SteerAttemptID
	result, err = store.resolveSteer(ctx, "agent-1", echo, "", "")
	require.NoError(t, err)
	require.True(t, result.matched)
	assert.Equal(t, first.Item.ID, result.accepted.ID)
	require.Len(t, result.snapshot.Items, 1)
	assert.Equal(t, "older", result.snapshot.Items[0].ID, "the echo resolves its own item")
}

func TestResolveSteerUncertainPreventsAdvanceAutoResume(t *testing.T) {
	t.Parallel()
	f := newEchoQueueFixture(t, true)
	f.enqueue(t, "e0")
	f.enqueue(t, "h1")
	var firstAttempt string
	f.dispatcher.onSteer = func(item DispatchItem) {
		if item.ID == "e0" {
			firstAttempt = item.SteerAttemptID
		}
	}
	_, err := f.manager.Steer(t.Context(), "agent-1", "e0")
	require.NoError(t, err)
	require.NotEmpty(t, firstAttempt)

	// While Advance steers h1 from a MANUAL pause, ResolveSteer marks earlier e0 uncertain.
	f.dispatcher.onSteer = func(item DispatchItem) {
		if item.ID == "h1" {
			matched, rerr := f.manager.ResolveSteer(t.Context(), "agent-1", agent.SteerResolution{
				InputID: "e0", AttemptID: firstAttempt, Outcome: agent.SteerOutcomeUncertain,
			}, "")
			require.NoError(t, rerr)
			require.True(t, matched)
		}
	}
	snapshot, err := f.manager.Advance(t.Context(), "agent-1", nil)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused, "Advance must not auto-resume after ResolveSteer marks an earlier steer uncertain")
}

func TestRowAtEchoSteerWithNativeReceiptFailsUncertainInsteadOfBypassingHoldForEcho(t *testing.T) {
	t.Parallel()
	for name, receipt := range map[string]agent.NativeInputReceipt{
		"full receipt": {SessionID: "session", InputID: "native-1", TurnID: "turn-1"},
		"session only": {SessionID: "session"},
		"input only":   {InputID: "native-1"},
		"turn only":    {TurnID: "turn-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			dispatcher := &recordingDispatcher{
				steering:     true,
				policy:       agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: false},
				steerReceipt: receipt,
			}
			observer := &recordingObserver{}
			manager := NewManager(store, dispatcher, observer)
			t.Cleanup(manager.StopAndWait)

			_, err := manager.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			_, err = store.Enqueue(t.Context(), NewItem{
				ID: "s1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "steer with receipt",
			})
			require.NoError(t, err)

			snapshot, err := manager.Steer(t.Context(), "agent-1", "s1")
			require.NoError(t, err)
			assert.True(t, snapshot.Paused, "a RowAtEcho steer returning a synchronous receipt must pause the queue as uncertain instead of bypassing holdForEcho")
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshot.PauseReason)
			assert.False(t, snapshot.ActiveTurn)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
			assert.Contains(t, snapshot.Items[0].Error, "synchronous native input receipt")

			observer.mu.Lock()
			assert.Empty(t, observer.accepted)
			observer.mu.Unlock()
			var count int
			require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

func TestRowAtEchoSteerControlRepliesAcceptNativeReceiptDirectly(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "native-reply", TurnID: "turn-1"}
			dispatcher := &recordingDispatcher{
				steering:     true,
				policy:       agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: false},
				steerReceipt: receipt,
			}
			observer := &recordingObserver{}
			manager := NewManager(store, dispatcher, observer)
			t.Cleanup(manager.StopAndWait)

			_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			_, err = store.Enqueue(t.Context(), NewItem{
				ID: "reply-1", AgentID: "agent-1", Kind: kind, Text: "answer",
			})
			require.NoError(t, err)

			snapshot, err := manager.Steer(t.Context(), "agent-1", "reply-1")
			require.NoError(t, err)
			assert.False(t, snapshot.Paused)
			assert.Empty(t, snapshot.Items)

			observer.mu.Lock()
			require.Len(t, observer.accepted, 1)
			assert.Equal(t, "reply-1", observer.accepted[0].ID)
			observer.mu.Unlock()

			var session, nativeID, turn string
			require.NoError(t, database.QueryRowContext(t.Context(), `
				SELECT m.agent_session_id, m.native_input_id, binding.native_turn_id
				FROM messages m JOIN native_agent_inputs binding USING (agent_id, agent_session_id, native_input_id)
				WHERE m.id = 'reply-1'`).Scan(&session, &nativeID, &turn))
			assert.Equal(t, receipt, agent.NativeInputReceipt{SessionID: session, InputID: nativeID, TurnID: turn})
		})
	}
}

func TestRowAtEchoSteerWithNativeReceiptAndTurnEndTiming(t *testing.T) {
	t.Parallel()

	t.Run("ErrTurnEnded requeues even when receipt is non-empty", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		dispatcher := &recordingDispatcher{
			steering:     true,
			policy:       agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: false},
			steerReceipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-1", TurnID: "turn-1"},
			steerFail:    ErrTurnEnded,
		}
		observer := &recordingObserver{}
		manager := NewManager(store, dispatcher, observer)
		t.Cleanup(manager.StopAndWait)

		_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
		require.NoError(t, err)
		_, err = store.Enqueue(t.Context(), NewItem{
			ID: "s1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "late steer",
		})
		require.NoError(t, err)

		snapshot, err := manager.Steer(t.Context(), "agent-1", "s1")
		require.NoError(t, err)
		assert.False(t, snapshot.Paused)
		require.Len(t, snapshot.Items, 1)
		assert.Equal(t, "s1", snapshot.Items[0].ID)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)

		observer.mu.Lock()
		assert.Empty(t, observer.accepted)
		observer.mu.Unlock()
		var count int
		require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
		assert.Zero(t, count)
	})

	t.Run("TurnEnded during steer with nil error and non-empty receipt marks uncertain", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		dispatcher := &recordingDispatcher{
			steering:     true,
			policy:       agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: false},
			steerReceipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-1", TurnID: "turn-1"},
		}
		observer := &recordingObserver{}
		manager := NewManager(store, dispatcher, observer)
		t.Cleanup(manager.StopAndWait)

		_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
		require.NoError(t, err)
		_, err = store.Enqueue(t.Context(), NewItem{
			ID: "s1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "raced steer",
		})
		require.NoError(t, err)
		dispatcher.onSteer = func(DispatchItem) {
			_, terr := manager.TurnEnded(t.Context(), "agent-1")
			require.NoError(t, terr)
		}

		snapshot, err := manager.Steer(t.Context(), "agent-1", "s1")
		require.NoError(t, err)
		assert.True(t, snapshot.Paused)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshot.PauseReason)
		assert.False(t, snapshot.ActiveTurn)
		require.Len(t, snapshot.Items, 1)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
		assert.Contains(t, snapshot.Items[0].Error, "synchronous native input receipt")

		observer.mu.Lock()
		assert.Empty(t, observer.accepted)
		observer.mu.Unlock()
		var count int
		require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
		assert.Zero(t, count)
	})
}

func TestConfirmedNativeReceiptAcceptsNonRowAtEchoSteerAfterTurnEnd(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "native-1", TurnID: "turn-1"}
	dispatcher := &recordingDispatcher{
		steering:     true,
		policy:       agent.SteerPolicy{RowAtEcho: false, SurvivesTurnEnd: false},
		steerReceipt: receipt,
	}
	observer := &recordingObserver{}
	manager := NewManager(store, dispatcher, observer)
	t.Cleanup(manager.StopAndWait)

	_, err := manager.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	_, err = store.Enqueue(t.Context(), NewItem{
		ID: "s1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "steer with receipt",
	})
	require.NoError(t, err)
	dispatcher.onSteer = func(DispatchItem) {
		_, terr := manager.TurnEnded(t.Context(), "agent-1")
		require.NoError(t, terr)
	}

	snapshot, err := manager.Steer(t.Context(), "agent-1", "s1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items, "a non-RowAtEcho steer confirmed by a NativeReceipt must be accepted instead of requeued")
	assert.False(t, snapshot.ActiveTurn)

	observer.mu.Lock()
	require.Len(t, observer.accepted, 1)
	assert.Equal(t, "s1", observer.accepted[0].ID)
	observer.mu.Unlock()

	var session, nativeID, turn string
	require.NoError(t, database.QueryRowContext(t.Context(), `
		SELECT m.agent_session_id, m.native_input_id, binding.native_turn_id
		FROM messages m JOIN native_agent_inputs binding USING (agent_id, agent_session_id, native_input_id)
		WHERE m.id = 's1'`).Scan(&session, &nativeID, &turn))
	assert.Equal(t, receipt, agent.NativeInputReceipt{SessionID: session, InputID: nativeID, TurnID: turn})
}

func TestPartialNativeReceiptRequeuesNonRowAtEchoSteerAfterTurnEnd(t *testing.T) {
	t.Parallel()
	for name, receipt := range map[string]agent.NativeInputReceipt{
		"session only": {SessionID: "session"},
		"input only":   {InputID: "native-1"},
		"turn only":    {TurnID: "turn-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			dispatcher := &recordingDispatcher{
				steering:     true,
				policy:       agent.SteerPolicy{RowAtEcho: false, SurvivesTurnEnd: false},
				steerReceipt: receipt,
			}
			observer := &recordingObserver{}
			manager := NewManager(store, dispatcher, observer)
			t.Cleanup(manager.StopAndWait)

			// Pause so the post-steer idle drain leaves the requeued item observable.
			_, err := manager.SetPaused(t.Context(), "agent-1", true)
			require.NoError(t, err)
			_, err = manager.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			_, err = store.Enqueue(t.Context(), NewItem{
				ID: "s1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "partial receipt steer",
			})
			require.NoError(t, err)
			dispatcher.onSteer = func(DispatchItem) {
				_, terr := manager.TurnEnded(t.Context(), "agent-1")
				require.NoError(t, terr)
			}

			snapshot, err := manager.Steer(t.Context(), "agent-1", "s1")
			require.NoError(t, err)
			assert.False(t, snapshot.ActiveTurn)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, "s1", snapshot.Items[0].ID)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
			assert.Empty(t, dispatcher.dispatches())

			observer.mu.Lock()
			assert.Empty(t, observer.accepted)
			observer.mu.Unlock()
			var count int
			require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

func TestWaitForRetiredDispatchWaitsForAdvanceAndPreempt(t *testing.T) {
	t.Parallel()

	t.Run("Advance", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		started := make(chan struct{})
		release := make(chan struct{})
		dispatcher := &recordingDispatcher{
			steering:     true,
			steerStarted: started,
			steerRelease: release,
		}
		manager := NewManager(store, dispatcher, &recordingObserver{})
		t.Cleanup(manager.StopAndWait)

		_, err := manager.TurnStarted(t.Context(), "agent-1", true)
		require.NoError(t, err)
		_, err = store.Enqueue(t.Context(), NewItem{
			ID: "a1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "advance",
		})
		require.NoError(t, err)

		advanceDone := make(chan error, 1)
		go func() {
			_, aerr := manager.Advance(t.Context(), "agent-1", nil)
			advanceDone <- aerr
		}()
		<-started
		require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))

		// While Advance is blocked in Steer, c.dispatches must remain non-zero so
		// WaitForRetiredDispatch cannot return before Advance finishes.
		select {
		case <-manager.coordinator("agent-1").dispatches.DoneChan():
			require.Fail(t, "WaitForRetiredDispatch channel was already closed while Advance was steering")
		default:
		}
		waitDone := make(chan error, 1)
		go func() {
			waitDone <- manager.WaitForRetiredDispatch(t.Context(), "agent-1")
		}()
		close(release)
		require.NoError(t, <-advanceDone)
		require.NoError(t, <-waitDone)
	})

	t.Run("Preempt rejects retired agent", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		dispatcher := &recordingDispatcher{preemption: true}
		manager := NewManager(store, dispatcher, &recordingObserver{})
		t.Cleanup(manager.StopAndWait)

		_, err := manager.TurnStarted(t.Context(), "agent-1", false)
		require.NoError(t, err)
		_, err = store.Enqueue(t.Context(), NewItem{
			ID: "p1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "preempt",
		})
		require.NoError(t, err)
		require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))

		_, err = manager.Preempt(t.Context(), "agent-1", "p1")
		assert.ErrorIs(t, err, ErrConflict)
		assert.Empty(t, dispatcher.interrupts())
	})
}
