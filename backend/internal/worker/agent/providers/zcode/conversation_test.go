package zcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// zcodeWireOf decodes the wire frame of a frame notification.
func zcodeWireOf(t *testing.T, line []byte) zcodeConversationWire {
	t.Helper()
	var envelope struct {
		Params zcodeConversationWire `json:"params"`
	}
	require.NoError(t, json.Unmarshal(line, &envelope))
	return envelope.Params
}

// zcodeLogicalFrame encodes a logical frame as ZCode sends it, and decodes it as
// LeapMux reads it.
func zcodeLogicalFrame(t *testing.T, frame map[string]any) zcodeConversationFrame {
	t.Helper()
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	var decoded zcodeConversationFrame
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

// A frame that arrives while no subscription is open, and a frame that does not
// decode, are dropped.
func TestZCodeConversationFrameWithoutASubscriptionIsDropped(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(zcodeTestReview(zcodeTestBundleDigest))))
	a.HandleOutput([]byte(`{"method":"` + NotifyConversationFrame + `","params":"not a frame"}`))
	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.conversation.pendingInteractions)
}

// A frame of another topic is dropped while the subscription is open.
func TestZCodeConversationFrameOfAnotherTopicIsDropped(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.Mu.Lock()
	a.conversation = zcodeConversation{topic: zcodeTestTopic, subscriptionID: "sub-1"}
	a.Mu.Unlock()
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"item-1", "pending_trust"})
	a.HandleOutput(zcodeCompleteWireLine(t, zcodeConversationTopicPrefix+"another-session", zcodeSnapshotFrame(review)))
	a.Mu.Lock()
	assert.Empty(t, a.conversation.pendingInteractions)
	a.Mu.Unlock()

	a.HandleOutput(zcodeCompleteWireLine(t, zcodeTestTopic, zcodeSnapshotFrame(review)))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	_, found := zcodeHookReviewInList(a.conversation.pendingInteractions, zcodeTestBundleDigest)
	assert.True(t, found)
}

// A fragment that contradicts its frame drops the whole frame: a wrong
// checksum, a wrong size, a repeated fragment, or a position out of range.
func TestZCodeConversationFragmentsThatContradictTheirFrame(t *testing.T) {
	t.Parallel()
	frame := zcodeDeltaFrame(zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"item-1", "pending_trust"}))

	t.Run("a whole frame in order", func(t *testing.T) {
		t.Parallel()
		var conversation zcodeConversation
		lines := zcodeFragmentWireLines(t, "lf", frame, 2)
		assembled, err := conversation.addFragment(zcodeWireOf(t, lines[0]))
		require.NoError(t, err)
		assert.Nil(t, assembled)
		assembled, err = conversation.addFragment(zcodeWireOf(t, lines[1]))
		require.NoError(t, err)
		var decoded zcodeConversationFrame
		require.NoError(t, json.Unmarshal(assembled, &decoded))
		conversation.observePendingInteractions(decoded)
		_, found := zcodeHookReviewInList(conversation.pendingInteractions, zcodeTestBundleDigest)
		assert.True(t, found)
		assert.Empty(t, conversation.fragments)
		assert.Zero(t, conversation.staged)
	})
	for name, corrupt := range map[string]func(first, second *zcodeConversationWire){
		"a wrong checksum": func(_, second *zcodeConversationWire) { second.Checksum.Value = "00000000" },
		"a checksum of the frame": func(first, second *zcodeConversationWire) {
			first.Checksum.Value, second.Checksum.Value = "00000000", "00000000"
		},
		"another fragment count":  func(_, second *zcodeConversationWire) { second.FragmentCount = 3 },
		"a repeated fragment":     func(_, second *zcodeConversationWire) { second.FragmentIndex = 0 },
		"a position out of range": func(_, second *zcodeConversationWire) { second.FragmentIndex = 2 },
		"another checksum method": func(_, second *zcodeConversationWire) { second.Checksum.Algorithm = "sha256" },
		"a frame that is too long": func(first, second *zcodeConversationWire) {
			first.LogicalBytes, second.LogicalBytes = zcodeFrameMaxBytes+1, zcodeFrameMaxBytes+1
		},
		"data that is not base64": func(_, second *zcodeConversationWire) { second.DataBase64 = "not base64!" },
		"more data than stated": func(first, second *zcodeConversationWire) {
			first.LogicalBytes, second.LogicalBytes = 4, 4
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var conversation zcodeConversation
			lines := zcodeFragmentWireLines(t, "lf", frame, 2)
			first, second := zcodeWireOf(t, lines[0]), zcodeWireOf(t, lines[1])
			corrupt(&first, &second)
			assembled, firstErr := conversation.addFragment(first)
			assert.Nil(t, assembled)
			assembled, secondErr := conversation.addFragment(second)
			assert.Nil(t, assembled)
			assert.True(t, firstErr != nil || secondErr != nil, "one of the fragments is refused")
			assert.Empty(t, conversation.fragments, "the frame is dropped")
			assert.Zero(t, conversation.staged)
		})
	}
	t.Run("too many staged bytes", func(t *testing.T) {
		t.Parallel()
		conversation := zcodeConversation{staged: zcodeFrameMaxStagedBytes}
		assembled, err := conversation.addFragment(zcodeWireOf(t, zcodeFragmentWireLines(t, "lf", frame, 2)[0]))
		require.Error(t, err)
		assert.Nil(t, assembled)
		assert.Empty(t, conversation.fragments)
	})
}

// The subscription keeps the newest list of pending interactions: a snapshot's,
// then each patch's. A frame that states no list keeps the one it has. A review
// counts only for its bundle.
func TestZCodeConversationKeepsTheNewestPendingInteractions(t *testing.T) {
	t.Parallel()
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"item-1", "pending_trust"})
	var conversation zcodeConversation
	conversation.observePendingInteractions(zcodeLogicalFrame(t, zcodeSnapshotFrame(review)))
	found, ok := zcodeHookReviewInList(conversation.pendingInteractions, zcodeTestBundleDigest)
	require.True(t, ok)
	assert.Equal(t, "workspace-hook-interaction:1", found.InteractionID)
	assert.Equal(t, []string{"item-1"}, found.pendingItemIDs())
	_, ok = zcodeHookReviewInList(conversation.pendingInteractions, "another-bundle")
	assert.False(t, ok)

	unrelated := zcodeLogicalFrame(t, map[string]any{"payload": map[string]any{"kind": "deltas", "deltas": []map[string]any{
		{"op": "row.appended", "row": map[string]any{"rowId": 1}},
	}}})
	conversation.observePendingInteractions(unrelated)
	_, ok = zcodeHookReviewInList(conversation.pendingInteractions, zcodeTestBundleDigest)
	assert.True(t, ok, "a frame without the list keeps it")

	later := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"item-2", "revoked"})
	frame := zcodeDeltaFrame(review)
	deltas := frame["payload"].(map[string]any)["deltas"].([]map[string]any)
	frame["payload"].(map[string]any)["deltas"] = append(deltas, map[string]any{
		"op": "state.updated", "patch": map[string]any{"pendingInteractions": []map[string]any{later}},
	})
	conversation.observePendingInteractions(zcodeLogicalFrame(t, frame))
	found, ok = zcodeHookReviewInList(conversation.pendingInteractions, zcodeTestBundleDigest)
	require.True(t, ok)
	assert.Equal(t, []string{"item-2"}, found.pendingItemIDs(), "a later patch replaces the list")

	conversation.observePendingInteractions(zcodeLogicalFrame(t, zcodeDeltaFrame()))
	_, ok = zcodeHookReviewInList(conversation.pendingInteractions, zcodeTestBundleDigest)
	assert.False(t, ok, "an empty list clears the review")
	_, ok = zcodeHookReviewInList(json.RawMessage(`not json`), zcodeTestBundleDigest)
	assert.False(t, ok)
}

// A stopped agent's subscription state goes without a request, because the
// subscription ended with the process.
func TestZCodeForgetConversationSendsNothing(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.conversation = zcodeConversation{topic: zcodeTestTopic, subscriptionID: "sub-1"}
	a.workflows.track("run-1", zcodeWorkflowTestRow("create-flow", "probe"))
	a.Mu.Unlock()
	a.forgetZCodeConversation()
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, zcodeConversation{}, a.conversation)
	assert.False(t, a.workflows.active())
	assert.Empty(t, stdin.Requests(t))
}
