package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestSteerEchoTrackerEarlyEchoDefersSharingUntilAccepted(t *testing.T) {
	t.Parallel()

	t.Run("cancelled early echo leaves lead turn unshared", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		lead, err := b.SendInput("work", nil)
		require.NoError(t, err)

		var tracker SteerEchoTracker
		dispatch, err := tracker.Begin(lead.SessionID, agent.SteerResolution{InputID: "q1", AttemptID: "a1"})
		require.NoError(t, err)

		// Early echo arrives before the steer RPC finishes.
		tracker.Resolve(b, "resolve", "early", "msg-1", agent.SteerOutcomeEchoed)
		// RPC fails, so tracker cancels the in-flight dispatch.
		tracker.Cancel(b, "resolve", dispatch)

		b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "still lead only"))
		require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
		testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

		assert.Empty(t, sink.SteerResolutions(), "cancelled early echo must not emit a steer resolution")
		messages := sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, lead.InputID, messages[0].NativeInputID, "cancelled early echo must not set sharingReply on the lead turn")
		assert.Equal(t, lead.InputID, messages[1].NativeInputID)
	})

	t.Run("accepted early echo commits sharing only for matching ID", func(t *testing.T) {
		t.Parallel()
		b, sink := newACPInputReplyBase(t)
		lead, err := b.SendInput("work", nil)
		require.NoError(t, err)

		var tracker SteerEchoTracker
		dispatch, err := tracker.Begin(lead.SessionID, agent.SteerResolution{InputID: "q1", AttemptID: "a1"})
		require.NoError(t, err)

		tracker.Resolve(b, "resolve", "early", "other-msg", agent.SteerOutcomeEchoed)
		tracker.Resolve(b, "resolve", "early", "matched-msg", agent.SteerOutcomeEchoed)
		require.NoError(t, tracker.Accept(b, "test", "resolve", dispatch, "matched-msg"))

		b.HandleOutput(acptest.Chunk(lead.SessionID, "agent_message_chunk", "shared output"))
		require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
		testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

		resolutions := sink.SteerResolutions()
		require.Len(t, resolutions, 1)
		assert.Equal(t, "matched-msg", resolutions[0].Receipt.InputID)
		assert.Equal(t, lead.TurnID, resolutions[0].Receipt.TurnID)
		messages := sink.Messages()
		require.Len(t, messages, 2)
		assert.Empty(t, messages[0].NativeInputID, "accepted early echo marks the lead turn as shared")
		assert.Equal(t, lead.TurnID, messages[0].NativeTurnID)
	})
}

func TestSteerEchoTrackerDropPendingResolvesInDeterministicIDOrder(t *testing.T) {
	t.Parallel()
	b, sink := newACPInputReplyBase(t)
	sessionID := b.CurrentSessionID()
	var tracker SteerEchoTracker

	for _, entry := range []struct {
		inputID string
		msgID   string
	}{
		{"q-c", "msg-c"},
		{"q-a", "msg-a"},
		{"q-b", "msg-b"},
	} {
		dispatch, err := tracker.Begin(sessionID, agent.SteerResolution{InputID: entry.inputID, AttemptID: "att"})
		require.NoError(t, err)
		require.NoError(t, tracker.Accept(b, "test", "resolve", dispatch, entry.msgID))
	}

	tracker.DropPending(b, "drop", agent.SteerOutcomeDropped)

	resolutions := sink.SteerResolutions()
	require.Len(t, resolutions, 3)
	assert.Equal(t, []string{"q-a", "q-b", "q-c"}, []string{
		resolutions[0].InputID,
		resolutions[1].InputID,
		resolutions[2].InputID,
	})
}

func TestSteerEchoTrackerRejectsBusyEmptyAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	b, _ := newACPInputReplyBase(t)
	sessionID := b.CurrentSessionID()
	var tracker SteerEchoTracker

	first, err := tracker.Begin(sessionID, agent.SteerResolution{InputID: "q1"})
	require.NoError(t, err)
	_, err = tracker.Begin(sessionID, agent.SteerResolution{InputID: "q2"})
	assert.ErrorIs(t, err, agent.ErrAgentBusy)

	assert.ErrorIs(t, tracker.Accept(b, "test", "resolve", first, ""), agent.ErrDeliveryUncertain)
	require.NoError(t, tracker.Accept(b, "test", "resolve", first, "msg-dup"))

	second, err := tracker.Begin(sessionID, agent.SteerResolution{InputID: "q2"})
	require.NoError(t, err)
	assert.ErrorIs(t, tracker.Accept(b, "test", "resolve", second, "msg-dup"), agent.ErrDeliveryUncertain)
	assert.True(t, tracker.DiscardPending(first))
	assert.False(t, tracker.DiscardPending(first))
}
