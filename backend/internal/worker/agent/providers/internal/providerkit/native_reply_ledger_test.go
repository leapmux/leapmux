package providerkit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type recordingReplySink struct {
	published  []agent.NativeReplyTurn
	retired    []retiredReplyCall
	publishErr error
	retireErr  error
}

type retiredReplyCall struct {
	receipt agent.NativeInputReceipt
	outcome leapmuxv1.AgentInputTurnOutcome
}

func (s *recordingReplySink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	s.published = append(s.published, turn)
	return nil
}

func (s *recordingReplySink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.retired = append(s.retired, retiredReplyCall{receipt: receipt, outcome: outcome})
	return s.retireErr
}

func TestNativeReplyLedger_HappyPathAndDeduplication(t *testing.T) {
	t.Parallel()
	var ledger NativeReplyLedger
	sink := &recordingReplySink{}
	receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

	ledger.BeginStart()
	ledger.FinishStart("turn-1", receipt, sink)
	assert.Equal(t, 1, ledger.Len())
	assert.Zero(t, ledger.PendingStarts())

	ledger.RecordItem("turn-1", "k1", nil)
	ledger.RecordItem("turn-1", "k1", nil)
	ledger.RecordItem("turn-1", "k2", &agent.PendingOutputError{Err: errors.New("queued")})
	ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

	assert.Zero(t, ledger.Len())
	assert.Empty(t, sink.retired)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:    []agent.NativeReplyBoundary{{InputID: "turn-1", AfterOrdinal: 1}},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "k1"},
			{Ordinal: 3, MessageKey: "k2"},
		},
	}}, sink.published)
}

func TestNativeReplyLedger_StartAfterCompletionRace(t *testing.T) {
	t.Parallel()
	var ledger NativeReplyLedger
	sink := &recordingReplySink{}

	ledger.BeginStart()
	// An autonomous foreign turn finishes while the start request is in flight.
	ledger.RecordItem("turn-foreign", "foreign-1", nil)
	ledger.Finish("turn-foreign", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

	// The requested turn also emits items and completes before the start response returns.
	ledger.RecordItem("turn-1", "item-1", nil)
	ledger.RecordItem("turn-1", "item-2", nil)
	ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

	assert.Empty(t, sink.published, "neither turn publishes before the start response identifies ownership")
	assert.Equal(t, 2, ledger.Len())

	receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
	ledger.FinishStart("turn-1", receipt, sink)

	assert.Zero(t, ledger.Len(), "the foreign turn is discarded once no starts remain")
	assert.Empty(t, sink.retired)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:    []agent.NativeReplyBoundary{{InputID: "turn-1", AfterOrdinal: 1}},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "item-1"},
			{Ordinal: 3, MessageKey: "item-2"},
		},
	}}, sink.published)
}

func TestNativeReplyLedger_StartAfterStreamClose(t *testing.T) {
	t.Parallel()

	t.Run("unobserved turn retires as unproved when start finishes after stream close", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		ledger.BeginStart()
		ledger.CloseStream()
		ledger.RetireAllAfterExit(sink)
		assert.Empty(t, sink.retired)

		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
		ledger.FinishStart("turn-1", receipt, sink)

		assert.Zero(t, ledger.Len())
		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{{
			receipt: receipt,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}}, sink.retired)
	})

	t.Run("already completed turn still publishes when start finishes after stream close", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		ledger.BeginStart()
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		ledger.RetireAllAfterExit(sink)
		assert.Empty(t, sink.published)
		assert.Empty(t, sink.retired)

		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
		ledger.FinishStart("turn-1", receipt, sink)

		assert.Zero(t, ledger.Len())
		assert.Empty(t, sink.retired)
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:    []agent.NativeReplyBoundary{{InputID: "turn-1", AfterOrdinal: 1}},
			Items:     []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "k1"}},
		}}, sink.published)
	})
}

func TestNativeReplyLedger_SteerBoundariesWithoutOpeningReceipt(t *testing.T) {
	t.Parallel()

	t.Run("publishes immediately on finish when no start is in flight", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		require.True(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-1", TurnID: "turn-1",
		}))
		ledger.RecordItem("turn-1", "k1", nil)
		require.True(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-2", TurnID: "turn-1",
		}))
		ledger.RecordItem("turn-1", "k2", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Zero(t, ledger.Len())
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs: []agent.NativeReplyBoundary{
				{InputID: "steer-1", AfterOrdinal: 1},
				{InputID: "steer-2", AfterOrdinal: 2},
			},
			Items: []agent.NativeReplyItem{
				{Ordinal: 2, MessageKey: "k1"},
				{Ordinal: 3, MessageKey: "k2"},
			},
		}}, sink.published)
	})

	t.Run("waits while a start is in flight and publishes on empty FinishStart", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		ledger.BeginStart()
		require.True(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-1", TurnID: "turn-1",
		}))
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		assert.Empty(t, sink.published)

		ledger.FinishStart("", agent.NativeInputReceipt{}, sink)
		assert.Zero(t, ledger.Len())
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:    []agent.NativeReplyBoundary{{InputID: "steer-1", AfterOrdinal: 1}},
			Items:     []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "k1"}},
		}}, sink.published)
	})

	t.Run("rejects invalid or mismatched steer boundaries without polluting the ledger", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger

		assert.False(t, ledger.RecordSteerBoundary("", agent.NativeInputReceipt{SessionID: "sess-1", InputID: "s1", TurnID: "turn-1"}))
		assert.False(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{SessionID: "", InputID: "s1", TurnID: "turn-1"}))
		assert.False(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{SessionID: "sess-1", InputID: "", TurnID: "turn-1"}))
		assert.False(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{SessionID: "sess-1", InputID: "s1", TurnID: "other"}))
		assert.Zero(t, ledger.Len())

		require.True(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "s1", TurnID: "turn-1",
		}))
		assert.False(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-other", InputID: "s2", TurnID: "turn-1",
		}))
	})
}

func TestNativeReplyLedger_RequireStart(t *testing.T) {
	t.Parallel()

	t.Run("retires as unproved without MarkStarted", func(t *testing.T) {
		t.Parallel()
		ledger := NativeReplyLedger{RequireStart: true}
		sink := &recordingReplySink{}
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		ledger.BeginStart()
		ledger.FinishStart("turn-1", receipt, sink)
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{{
			receipt: receipt,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}}, sink.retired)
	})

	t.Run("publishes exact reply after MarkStarted", func(t *testing.T) {
		t.Parallel()
		ledger := NativeReplyLedger{RequireStart: true}
		sink := &recordingReplySink{}
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		ledger.BeginStart()
		ledger.MarkStarted("turn-1")
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.FinishStart("turn-1", receipt, sink)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Empty(t, sink.retired)
		require.Len(t, sink.published, 1)
		assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "k1"}}, sink.published[0].Items)
	})
}

func TestNativeReplyLedger_DurableTurnID(t *testing.T) {
	t.Parallel()
	ledger := NativeReplyLedger{
		RequireStart:  true,
		DurableTurnID: func(wireTurnID string) string { return "proc:" + wireTurnID },
	}
	sink := &recordingReplySink{}

	ledger.BeginStart()
	ledger.MarkStarted("turn_1")
	entry := ledger.Lookup("turn_1")
	require.NotNil(t, entry)
	assert.Equal(t, "proc:turn_1", entry.TurnID())

	// Steer boundary validates against the durable turn ID.
	assert.False(t, ledger.RecordSteerBoundary("turn_1", agent.NativeInputReceipt{
		SessionID: "sess-1", InputID: "steer:1", TurnID: "turn_1",
	}))
	require.True(t, ledger.RecordSteerBoundary("turn_1", agent.NativeInputReceipt{
		SessionID: "sess-1", InputID: "steer:1", TurnID: "proc:turn_1",
	}))

	ledger.RecordItem("turn_1", "k1", nil)
	assert.Equal(t, 1, entry.ItemCount())
	assert.True(t, entry.HasKey("k1"))

	receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "proc:turn_1", TurnID: "proc:turn_1"}
	ledger.FinishStart("turn_1", receipt, sink)
	ledger.Finish("turn_1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

	assert.Empty(t, sink.retired)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "sess-1",
		TurnID:    "proc:turn_1",
		Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: "proc:turn_1", AfterOrdinal: 1},
			{InputID: "steer:1", AfterOrdinal: 1},
		},
		Items: []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "k1"}},
	}}, sink.published)
}

func TestNativeReplyLedger_PublishErrorFallsBackToRetirement(t *testing.T) {
	t.Parallel()

	t.Run("non-retained publish error retires opening and steer receipts as unproved", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{publishErr: errors.New("storage failed")}
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		ledger.BeginStart()
		ledger.FinishStart("turn-1", receipt, sink)
		require.True(t, ledger.RecordSteerBoundary("turn-1", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-1", TurnID: "turn-1",
		}))
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Zero(t, ledger.Len())
		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{
			{
				receipt: agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"},
				outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			},
			{
				receipt: agent.NativeInputReceipt{SessionID: "sess-1", InputID: "steer-1", TurnID: "turn-1"},
				outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			},
		}, sink.retired)
	})

	t.Run("retained pending publish error does not fall back to retirement", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{publishErr: &agent.PendingOutputError{Err: errors.New("queued")}}
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		ledger.BeginStart()
		ledger.FinishStart("turn-1", receipt, sink)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Zero(t, ledger.Len())
		assert.Empty(t, sink.retired)
	})

	t.Run("failed item write or MarkUnproved retires completed turn as unproved", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}
		r1 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
		r2 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-2", TurnID: "turn-2"}

		ledger.BeginStart()
		ledger.FinishStart("turn-1", r1, sink)
		ledger.RecordItem("turn-1", "k1", errors.New("persist failed"))
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		ledger.BeginStart()
		ledger.FinishStart("turn-2", r2, sink)
		ledger.MarkUnproved("turn-2")
		ledger.Finish("turn-2", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{
			{receipt: r1, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
			{receipt: r2, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
		}, sink.retired)
	})

	t.Run("interrupted and failed outcomes retire with their exact outcome", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}
		r1 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "t1", TurnID: "t1"}
		r2 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "t2", TurnID: "t2"}

		ledger.BeginStart()
		ledger.FinishStart("t1", r1, sink)
		ledger.Finish("t1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, sink)

		ledger.BeginStart()
		ledger.FinishStart("t2", r2, sink)
		ledger.Finish("t2", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink)

		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{
			{receipt: r1, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
			{receipt: r2, outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
		}, sink.retired)
	})
}

func TestNativeReplyLedger_DeterministicSortedRetirement(t *testing.T) {
	t.Parallel()
	wantTurns := []string{"turn_1", "turn_2", "turn_3", "turn_4", "turn_5", "turn_6"}

	t.Run("RetireAllAfterExit retires in sorted wire turn ID order", func(t *testing.T) {
		t.Parallel()
		for range 20 {
			var ledger NativeReplyLedger
			sink := &recordingReplySink{}
			for i := len(wantTurns) - 1; i >= 0; i-- {
				turnID := wantTurns[i]
				ledger.BeginStart()
				ledger.FinishStart(turnID, agent.NativeInputReceipt{
					SessionID: "sess-1", InputID: turnID, TurnID: turnID,
				}, sink)
			}
			ledger.RetireAllAfterExit(sink)
			require.Len(t, sink.retired, len(wantTurns))
			got := make([]string, len(sink.retired))
			for i, call := range sink.retired {
				got[i] = call.receipt.TurnID
			}
			assert.Equal(t, wantTurns, got)
		}
	})

	t.Run("FinishStart sweeps steer-only turns in sorted wire turn ID order", func(t *testing.T) {
		t.Parallel()
		for range 20 {
			var ledger NativeReplyLedger
			sink := &recordingReplySink{}
			ledger.BeginStart()
			for i := len(wantTurns) - 1; i >= 0; i-- {
				turnID := wantTurns[i]
				require.True(t, ledger.RecordSteerBoundary(turnID, agent.NativeInputReceipt{
					SessionID: "sess-1", InputID: "steer_" + turnID, TurnID: turnID,
				}))
				ledger.Finish(turnID, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
			}
			ledger.FinishStart("", agent.NativeInputReceipt{}, sink)
			require.Len(t, sink.published, len(wantTurns))
			got := make([]string, len(sink.published))
			for i, turn := range sink.published {
				got[i] = turn.TurnID
			}
			assert.Equal(t, wantTurns, got)
		}
	})

	t.Run("nil sink settles turns without panicking", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		ledger.BeginStart()
		ledger.FinishStart("turn_1", agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn_1", TurnID: "turn_1"}, nil)
		ledger.RetireAllAfterExit(nil)
		ledger.RetireInput(nil, agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn_1", TurnID: "turn_1"}, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		assert.Zero(t, ledger.Len())
	})
}

func TestNativeReplyLedger_BoundaryGuardsAndMultipleStarts(t *testing.T) {
	t.Parallel()

	t.Run("empty turn IDs and untracked lookups do not create entries", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		assert.Nil(t, ledger.Lookup(""))
		assert.Nil(t, ledger.Turn(""))
		assert.Nil(t, ledger.Register(""))
		assert.Nil(t, ledger.Turn("untracked"))

		ledger.MarkStarted("")
		ledger.MarkUnproved("")
		ledger.RecordItem("", "k1", nil)
		ledger.Finish("", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		ledger.Finish("untracked", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		assert.Zero(t, ledger.Len())

		// Lookup must not create an entry even while a start request is in flight.
		ledger.BeginStart()
		assert.Nil(t, ledger.Lookup("untracked"))
		assert.Zero(t, ledger.Len())
		ledger.FinishStart("", agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}, sink)
		assert.Zero(t, ledger.PendingStarts())
		assert.Zero(t, ledger.Len())

		// Register returns an existing entry without resetting its state.
		first := ledger.Register("turn-1")
		require.NotNil(t, first)
		first.RecordItem("k1", nil)
		second := ledger.Register("turn-1")
		assert.Same(t, first, second)
		assert.Equal(t, 1, second.ItemCount())
	})

	t.Run("FinishStart without BeginStart registers without negative starts", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		ledger.FinishStart("turn-1", receipt, sink)
		assert.Zero(t, ledger.PendingStarts())
		assert.Equal(t, 1, ledger.Len())

		entry := ledger.Lookup("turn-1")
		require.NotNil(t, entry)
		entry.MarkUnproved()
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		assert.Zero(t, ledger.Len())
		assert.Empty(t, sink.published)
		assert.Equal(t, []retiredReplyCall{{
			receipt: receipt,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}}, sink.retired)
	})

	t.Run("unspecified outcome defaults to unproved and second finish is ignored", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}
		r1 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
		r2 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-2", TurnID: "turn-2"}

		ledger.BeginStart()
		ledger.FinishStart("turn-1", r1, sink)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED, sink)

		// While a start is in flight, a completed turn stays in the ledger:
		// a late Finish, RecordSteerBoundary, or retained RecordItem must not alter it.
		ledger.BeginStart()
		ledger.RecordItem("turn-2", "k1", nil)
		ledger.Finish("turn-2", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		ledger.Finish("turn-2", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink)
		ledger.RecordItem("turn-2", "k2", nil)
		assert.False(t, ledger.RecordSteerBoundary("turn-2", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-late", TurnID: "turn-2",
		}))
		ledger.FinishStart("turn-2", r2, sink)

		assert.Equal(t, []retiredReplyCall{{
			receipt: r1,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}}, sink.retired)
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "sess-1",
			TurnID:    "turn-2",
			Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:    []agent.NativeReplyBoundary{{InputID: "turn-2", AfterOrdinal: 1}},
			Items:     []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "k1"}},
		}}, sink.published)
	})

	t.Run("overlapping starts defer untracked sweep until the final start finishes", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		sink := &recordingReplySink{}

		ledger.BeginStart()
		ledger.BeginStart()
		assert.Equal(t, 2, ledger.PendingStarts())

		ledger.RecordItem("turn-foreign", "f1", nil)
		ledger.Finish("turn-foreign", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		require.True(t, ledger.RecordSteerBoundary("turn-steer", agent.NativeInputReceipt{
			SessionID: "sess-1", InputID: "steer-1", TurnID: "turn-steer",
		}))
		ledger.Finish("turn-steer", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)

		r1 := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}
		ledger.RecordItem("turn-1", "k1", nil)
		ledger.Finish("turn-1", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, sink)
		ledger.FinishStart("turn-1", r1, sink)

		assert.Equal(t, 1, ledger.PendingStarts())
		assert.Equal(t, 2, ledger.Len(), "untracked turns wait while another start is in flight")
		require.Len(t, sink.published, 1)
		assert.Equal(t, "turn-1", sink.published[0].TurnID)

		ledger.FinishStart("", agent.NativeInputReceipt{}, sink)
		assert.Zero(t, ledger.PendingStarts())
		assert.Zero(t, ledger.Len())
		require.Len(t, sink.published, 2)
		assert.Equal(t, "turn-steer", sink.published[1].TurnID)
	})

	t.Run("RetireInput tolerates sink errors", func(t *testing.T) {
		t.Parallel()
		var ledger NativeReplyLedger
		receipt := agent.NativeInputReceipt{SessionID: "sess-1", InputID: "turn-1", TurnID: "turn-1"}

		failingSink := &recordingReplySink{retireErr: errors.New("retire failed")}
		ledger.RetireInput(failingSink, receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		require.Len(t, failingSink.retired, 1)

		pendingSink := &recordingReplySink{retireErr: &agent.PendingOutputError{Err: errors.New("queued")}}
		ledger.RetireInput(pendingSink, receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED)
		require.Len(t, pendingSink.retired, 1)
	})
}
