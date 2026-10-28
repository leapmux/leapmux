package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSessionStateUpdatesStoreNoRow(t *testing.T) {
	t.Parallel()
	for _, kind := range contracts.ACPSessionStateUpdateMembers {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			c := b.main()
			c.out.appendAssistant("before", "answer")
			raw, err := json.Marshal(map[string]string{"sessionUpdate": kind})
			require.NoError(t, err)
			assert.False(t, c.handleUpdate(acpUpdateHeader{SessionUpdate: kind}, raw))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.ProgressUpdates())
			assert.Equal(t, "before", b.TurnAssistantTextForTest().String())
		})
	}
}

func TestSessionStateModeRecordStillPersistsThroughTheBase(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	update := json.RawMessage(`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`)
	b.handleACPUpdate(update)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(update), string(sink.Messages()[0].Content))
}

func TestSessionNotificationMetadataUsesTheOuterCurrentSessionMeta(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetSessionIDForTest("session-1")
	var outer, inner map[string]json.RawMessage
	var outerKind string
	var calls int
	b.HooksForTest().SessionNotificationMetadata = func(updateType string, metadata map[string]json.RawMessage) {
		outerKind = updateType
		outer = metadata
		calls++
	}
	b.HooksForTest().SessionMetadataHandler = func(_ string, metadata map[string]json.RawMessage, _ json.RawMessage) bool {
		inner = metadata
		return false
	}
	frame := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"parentToolCallId":"child-1"}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"120 in, 30 out"}}}}}`
	b.HandleOutput([]byte(frame))

	require.Equal(t, 1, calls)
	assert.Equal(t, "agent_message_chunk", outerKind, "the hook reads the kind of update that carried the metadata")
	assert.JSONEq(t, `{"openhands.dev/metrics":{"status_line":"120 in, 30 out"}}`, string(outer["field_meta"]))
	assert.JSONEq(t, `"child-1"`, string(inner["parentToolCallId"]))
	assert.NotContains(t, outer, "parentToolCallId")
	assert.NotContains(t, inner, "field_meta")

	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"999 in, 999 out"}}}}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}}}`))
	assert.Equal(t, 1, calls, "another session and absent metadata must not invoke the hook")
}

func TestProviderNotificationsKeepRawByteOwnershipAndTheirResolvedSource(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	b.beginSessionUpdates()
	raw := json.RawMessage(`{"native":"original"}`)
	var received []string
	resolveCalls := 0
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw: raw,
		ResolveSession: func(json.RawMessage) (string, bool) {
			resolveCalls++
			return b.CurrentSessionID(), true
		},
		Dispatch: func(_ string, frame json.RawMessage) { received = append(received, string(frame)) },
	}))
	copy(raw, `{"native":"mutated!"}`)
	b.finishSessionUpdates()
	assert.Equal(t, []string{`{"native":"original"}`}, received)
	assert.Equal(t, 1, resolveCalls)

	b.beginSessionUpdates()
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw: json.RawMessage(`{"native":"outgoing"}`),
		ResolveSession: func(json.RawMessage) (string, bool) {
			resolveCalls++
			return b.CurrentSessionID(), true
		},
		Dispatch: func(_ string, frame json.RawMessage) { received = append(received, string(frame)) },
	}))
	b.SetSessionIDForTest("replacement-root")
	b.finishSessionUpdates()
	assert.Equal(t, []string{`{"native":"original"}`}, received, "a resolved notification cannot acquire the replacement owner")
	assert.Equal(t, 2, resolveCalls, "a resolved source is never queried again")
}

func TestProviderAndStandardNotificationsReplayInReceivedOrder(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	var order []string
	b.hooks.SessionUpdateHandler = func(_ string, _ agent.ProviderServices, raw json.RawMessage) bool {
		order = append(order, string(raw))
		return true
	}
	b.beginSessionUpdates()
	first := `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first"}}`
	last := `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"last"}}`
	b.handleACPSessionUpdate(sessionUpdate(t, "session-1", first))
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw:            json.RawMessage(`native between`),
		ResolveSession: func(json.RawMessage) (string, bool) { return "session-1", true },
		Dispatch:       func(_ string, raw json.RawMessage) { order = append(order, string(raw)) },
	}))
	b.handleACPSessionUpdate(sessionUpdate(t, "session-1", last))
	assert.Empty(t, order)
	b.finishSessionUpdates()
	assert.Equal(t, []string{first, "native between", last}, order)
}

func TestProviderNotificationCanResolveAfterAnEarlierBufferedAnnouncement(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	var order []string
	announced := false
	b.beginSessionUpdates()
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw:            json.RawMessage(`announcement`),
		ResolveSession: func(json.RawMessage) (string, bool) { return "replacement-root", true },
		Dispatch: func(_ string, raw json.RawMessage) {
			announced = true
			order = append(order, string(raw))
		},
	}))
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw:            json.RawMessage(`dependent event`),
		ResolveSession: func(json.RawMessage) (string, bool) { return "replacement-root", announced },
		Dispatch:       func(_ string, raw json.RawMessage) { order = append(order, string(raw)) },
	}))
	b.sessionUpdates.mu.Lock()
	b.flushPreviousSessionUpdates()
	b.sessionUpdates.mu.Unlock()
	assert.Empty(t, order)
	b.SetSessionIDForTest("replacement-root")
	b.finishSessionUpdates()
	assert.Equal(t, []string{"announcement", "dependent event"}, order)
}

func TestProviderNotificationsRejectIncompleteHandlersAndUnknownOwners(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	called := false
	dispatch := func(string, json.RawMessage) { called = true }
	assert.False(t, b.RouteSessionNotification(SessionNotification{Dispatch: dispatch}))
	assert.False(t, b.RouteSessionNotification(SessionNotification{ResolveSession: func(json.RawMessage) (string, bool) { return "session-1", true }}))
	b.beginSessionUpdates()
	assert.True(t, b.RouteSessionNotification(SessionNotification{
		Raw:            json.RawMessage(`unowned`),
		ResolveSession: func(json.RawMessage) (string, bool) { return "", false },
		Dispatch:       dispatch,
	}))
	b.finishSessionUpdates()
	assert.False(t, called)
	assert.Empty(t, b.sessionUpdates.pending)
}

func TestBufferedChildNotificationCannotAcquireAReassignedRoot(t *testing.T) {
	t.Parallel()
	b, _ := newChildRouteBase(t)
	require.True(t, b.AttachChildSession(ChildSessionTarget{RootSessionID: "session-1", SessionID: "child", RowKey: "old-row"}))
	called := false
	b.beginSessionUpdates()
	require.True(t, b.RouteSessionNotification(SessionNotification{
		Raw:            json.RawMessage(`old child`),
		ResolveSession: func(json.RawMessage) (string, bool) { return "child", true },
		Dispatch:       func(string, json.RawMessage) { called = true },
	}))
	b.SetSessionIDForTest("replacement-root")
	require.False(t, b.ServesSession("child"))
	require.True(t, b.AttachChildSession(ChildSessionTarget{RootSessionID: "replacement-root", SessionID: "child", RowKey: "new-row"}))
	b.finishSessionUpdates()
	assert.False(t, called)
}
