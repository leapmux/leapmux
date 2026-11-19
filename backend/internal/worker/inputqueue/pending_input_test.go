package inputqueue

import (
	"context"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoTurnReceiptReleasesOnlyItsDispatch(t *testing.T) {
	for _, nativeTurn := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeTurn), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			for _, id := range []string{"first", "next"} {
				_, err := store.Enqueue(t.Context(), NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
				require.NoError(t, err)
			}
			first, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, first)
			if nativeTurn {
				_, _, err = store.TurnStarted(t.Context(), "agent-1", false)
				require.NoError(t, err)
			}
			_, snapshot, err := store.Accept(t.Context(), *first, DispatchResult{})
			require.NoError(t, err)
			assert.Equal(t, nativeTurn, snapshot.ActiveTurn)
			next, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			if nativeTurn {
				assert.Nil(t, next)
			} else {
				require.NotNil(t, next)
				assert.Equal(t, "next", next.Item.ID)
			}
		})
	}
}

func TestNoTurnReceiptPreservesIndependentlyReportedNativeTurn(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	_, err := store.Enqueue(t.Context(), NewItem{ID: "pending", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued native prompt"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	_, snapshot, err := store.Accept(t.Context(), *prepared, DispatchResult{})
	require.NoError(t, err)
	assert.True(t, snapshot.ActiveTurn)
	assert.True(t, snapshot.ActiveTurnSteerable)
}

func TestPendingRefusalRequeuesWithoutInventingNativeActivity(t *testing.T) {
	for _, nativeTurn := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeTurn), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			requeuer, ok := any(store).(interface {
				RequeuePending(context.Context, string, string) (Snapshot, error)
			})
			require.True(t, ok, "pending admission needs its own refusal outcome")
			_, err := store.Enqueue(t.Context(), NewItem{ID: "next", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next"})
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			if nativeTurn {
				_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
				require.NoError(t, err)
			}
			snapshot, err := requeuer.RequeuePending(t.Context(), "agent-1", "next")
			require.NoError(t, err)
			assert.Equal(t, nativeTurn, snapshot.ActiveTurn)
			assert.Equal(t, nativeTurn, snapshot.ActiveTurnSteerable)
			assert.False(t, snapshot.Paused)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
			assert.Empty(t, snapshot.Items[0].Error)
		})
	}
}

func TestAcceptedSteeringPreservesTheRunningTurn(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	_, _, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "steer", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "steer the current turn"})
	require.NoError(t, err)
	prepared, before, err := store.PrepareSteer(ctx, "agent-1", "steer", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Positive(t, prepared.ReservedSeq)
	accepted, after, err := store.Accept(ctx, *prepared, DispatchResult{Steering: true})
	require.NoError(t, err)
	assert.Positive(t, accepted.Seq)
	assert.True(t, after.ActiveTurn)
	assert.True(t, after.ActiveTurnSteerable)
	assert.Greater(t, after.Revision, before.Revision)
	assert.Empty(t, after.Items)
}
