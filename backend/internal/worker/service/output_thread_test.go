package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func decodeNotifWrapper(t *testing.T, content []byte, compression leapmuxv1.ContentCompression) notifThreadWrapper {
	t.Helper()
	raw, err := msgcodec.Decompress(content, compression)
	require.NoError(t, err)

	var wrapper notifThreadWrapper
	require.NoError(t, json.Unmarshal(raw, &wrapper))
	return wrapper
}

// setupNotifThreadTest stands up a worker service with one agent and returns
// the sink + a row-listing helper bound to that agent. Used by the
// notification-threading tests below to remove repeated CreateAgent /
// NewRootSink / ListMessagesByAgentID boilerplate.
func setupNotifThreadTest(t *testing.T, provider leapmuxv1.AgentProvider) (agent.ProviderServices, func() []db.Message) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
	}))

	sink := newTestRootOutputSink(t, svc.Output, "agent-1", provider)
	listRows := func() []db.Message {
		t.Helper()
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
			AgentID: "agent-1",
			Seq:     0,
			Limit:   20,
		})
		require.NoError(t, err)
		return rows
	}
	return sink, listRows
}

func TestNotificationThreading_MergesOnlyAdjacentNotifications(t *testing.T) {
	t.Parallel()

	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	firstNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	secondNotif, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, firstNotif)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, secondNotif)

	rows := listRows()
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"context_cleared", "interrupted"}, types(t, wrapper.Messages))
}

// TestNotificationThreading_CrossSourceProducesSeparateThreads verifies
// that adjacent notifications with different sources do not consolidate
// into one thread. An AGENT-source system notification followed by a
// LEAPMUX-source platform notification must produce two separate
// notification rows, each carrying a truthful per-thread source.
func TestNotificationThreading_CrossSourceProducesSeparateThreads(t *testing.T) {
	t.Parallel()

	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	agentNotif, err := json.Marshal(map[string]any{"type": "system", "subtype": "status", "status": "compacting"})
	require.NoError(t, err)
	leapmuxNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agentNotif)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, leapmuxNotif)

	rows := listRows()
	require.Len(t, rows, 2, "cross-source adjacent notifications must not consolidate")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[1].Source)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[1].Content, rows[1].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
}

func TestNotificationThreading_WorkerRetryResolutionUpdatesTheNativeRetry(t *testing.T) {
	t.Parallel()
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved,
	})
	rows := listRows()
	require.Len(t, rows, 1, "the worker resolution must update the existing retry row")
	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 1)
	assert.JSONEq(t, `{"type":"api_retry_resolved"}`, string(wrapper.Messages[0]))
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
}

func TestNotificationThreading_RetryResolutionPreservesBoundaries(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"empty", "ordinary notice", "answer", "session change", "worker retry", "intervening source", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			sink.UpdateSessionID("session-one")
			if scenario != "empty" && scenario != "ordinary notice" {
				if scenario == "worker retry" {
					sink.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry", "count": 1})
				} else {
					persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
				}
			}
			switch scenario {
			case "ordinary notice", "intervening source":
				sink.PersistLeapMuxNotification(map[string]interface{}{"type": "agent_status", "message": "A separate notice"})
			case "answer":
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`)}, agent.SpanInfo{}))
			case "session change":
				sink.UpdateSessionID("session-two")
			}
			before := listRows()
			resolve := func() {
				sink.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry_resolved"})
			}
			resolve()
			after := listRows()
			switch scenario {
			case "worker retry", "intervening source", "duplicate":
				require.Len(t, after, len(before))
				resolved := after[len(after)-1]
				assert.Equal(t, before[0].ID, resolved.ID)
				assert.Equal(t, before[0].Source, resolved.Source)
				wrapper := decodeNotifWrapper(t, resolved.Content, resolved.ContentCompression)
				require.Len(t, wrapper.Messages, 1)
				assert.JSONEq(t, `{"type":"api_retry_resolved"}`, string(wrapper.Messages[0]))
				if scenario == "intervening source" {
					assert.Equal(t, before[1], after[0], "the ordinary notice keeps its own row and source")
				}
				resolve()
				assert.Equal(t, after, listRows(), "a repeated resolution changes nothing")
			default:
				assert.Equal(t, before, after, "a resolution cannot create a row or cross a transcript boundary")
			}
		})
	}
}

func TestNotificationThreading_StaleSinkCannotResolveTheCurrentRetry(t *testing.T) {
	t.Parallel()
	svc, old := setupRootSink(t, "retry-owner")
	old.UpdateSessionID("same-native-session")
	current := newTestRootOutputSink(t, svc.Output, "retry-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	current.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry", "count": 1})
	list := func() []db.Message {
		rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "retry-owner"})
		require.NoError(t, err)
		return rows
	}
	before := list()
	require.Len(t, before, 1)
	old.PersistLeapMuxNotification(map[string]interface{}{"type": "api_retry_resolved"})
	assert.Equal(t, before, list())
}

func TestNotificationThreading_NonNotificationBreaksAdjacency(t *testing.T) {
	t.Parallel()

	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	firstNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	secondNotif, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)
	assistantMsg, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello"}},
		},
	})
	require.NoError(t, err)

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, firstNotif)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: assistantMsg}, agent.SpanInfo{}))
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, secondNotif)

	rows := listRows()
	require.Len(t, rows, 3)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[2].Content, rows[2].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
	assert.Equal(t, "context_cleared", msgType(t, firstWrapper.Messages[0]))
	assert.Equal(t, "interrupted", msgType(t, secondWrapper.Messages[0]))
}

// TestRelaunchOnExitPreservesNotificationThread is the regression for the duplicate
// settings-change notifications (Issue 1). A model/effort switch RELAUNCHES the agent;
// the old process stopping fires the runner's onExit handler, which must drop the
// dying process's pending control_requests WITHOUT clearing the in-memory notification
// thread -- otherwise the notification persisted after the relaunch lands in a fresh
// thread and can't consolidate with (cancel) the one before it. The bug was onExit
// calling the full ClearAgentRuntimeState (which clears lastNotifThread via
// CleanupAgent); the fix routes onExit through ClearPendingControlRequests instead.
func TestRelaunchOnExitPreservesNotificationThread(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	listRows := func() []db.Message {
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 20})
		require.NoError(t, err)
		return rows
	}

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("sonnet", "haiku")))
	// Exactly what the relaunch's onExit handler now does (runner.go): drop pending
	// control requests, but leave the notification thread intact.
	svc.Output.ClearPendingControlRequests("agent-1")
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("haiku", "opus[1m]")))

	rows := listRows()
	require.Len(t, rows, 1, "the relaunch's onExit must NOT split the notification thread into two rows")
	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 1, "the two model changes consolidate into one settings_changed")
	var merged struct {
		Changes map[string]struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(wrapper.Messages[0], &merged))
	assert.Equal(t, "sonnet", merged.Changes["model"].Old)
	assert.Equal(t, "opus[1m]", merged.Changes["model"].New, "sonnet->haiku->opus[1m] merges to sonnet->opus[1m]")

	// Contrast: the PERMANENT-teardown cleanup DOES clear the thread, so a later
	// notification correctly starts a fresh standalone row.
	svc.Output.ClearAgentRuntimeState("agent-1", CleanupTabClosed)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("opus[1m]", "haiku")))
	assert.Len(t, listRows(), 2, "ClearAgentRuntimeState clears the thread, so the next notification is a new row")
}

func TestNotificationThreading_CodexStartupStatusConsolidatesInWrapper(t *testing.T) {
	t.Parallel()

	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	starting := raw(t, codexStartupStatus("codex_apps", "starting", nil))
	ready := raw(t, codexStartupStatus("codex_apps", "ready", nil))
	settingsChanged, err := json.Marshal(map[string]any{
		"type": "settings_changed",
		"changes": map[string]any{
			"permissionMode": map[string]any{"old": "on-request", "new": "never"},
		},
	})
	require.NoError(t, err)

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, starting)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, ready)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, settingsChanged)

	rows := listRows()
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"mcpServer/startupStatus/updated", "settings_changed"}, types(t, wrapper.Messages))

	startup := parseRaw(t, wrapper.Messages[0])
	params := startup["params"].(map[string]interface{})
	assert.Equal(t, "codex_apps", params["name"])
	assert.Equal(t, "ready", params["status"])
}

// TestNotificationThreading_RepeatedIdenticalProviderScopedSkipsWrite verifies
// that a repeated ProviderScoped notification does not bump the row sequence
// when consolidation leaves the message list unchanged.
func TestNotificationThreading_RepeatedIdenticalProviderScopedSkipsWrite(t *testing.T) {
	t.Parallel()

	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	payload := raw(t, codexStartupStatus("codex_apps", "failed", "connection failed"))

	// The first notification opens a standalone thread and is broadcast.
	broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload})
	require.NoError(t, err)
	assert.True(t, broadcast, "the first notification is broadcast to the frontend")
	rows := listRows()
	require.Len(t, rows, 1)
	seqAfterFirst := rows[0].Seq

	// Each repeat collapses byte-identically into the tail: no DB write AND no
	// broadcast. The broadcast=false return is what keeps the thinking-token
	// reset decorator in lockstep with the frontend, which never clears here.
	for i := 0; i < 5; i++ {
		broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload})
		require.NoError(t, err)
		assert.False(t, broadcast,
			"an identical ProviderScoped notification collapses and must not report a broadcast")
	}
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, seqAfterFirst, rows[0].Seq,
		"identical ProviderScoped notifications must not bump the row's seq")
}

// notifThreadWrapper's `type` tag is the wrapper's discriminator, which the browser
// reads back through NOTIFICATION_FIELD. A Go struct tag cannot hold the generated
// constant, so this test is what keeps the tag and the contract together -- and it is
// what lets wrapNotifContent's fallback spell the key from the constant.
//
// `old_seqs` and `messages` are LeapMux's own wrapper fields, which no contract table
// holds, so they stay literals here.
func TestNotifThreadWrapperTagsComeFromTheContract(t *testing.T) {
	t.Parallel()

	tags := make([]string, 0, 3)
	for _, field := range reflect.VisibleFields(reflect.TypeOf(notifThreadWrapper{})) {
		tags = append(tags, field.Tag.Get("json"))
	}

	assert.Equal(t, []string{contracts.NotificationFieldType, "old_seqs,omitempty", "messages"}, tags)
}

// The wrapper's `type` key is spelled twice: once as the struct tag that the success
// path uses, and once in the hand-written fallback that wrapNotifContent returns when
// the caller's bytes are not valid JSON. This test holds the two branches to the same
// bytes, and the test above holds the tag to the contract.
//
// The fallback cannot come from json.Marshal, because the marshal that just failed is
// the reason it runs.
func TestWrapNotifContentFallbackMatchesTheStruct(t *testing.T) {
	t.Parallel()

	// json.RawMessage validates what it carries, so invalid bytes reach the fallback.
	fallback := wrapNotifContent([]byte(`{"type":`))

	encoded, err := json.Marshal(notifThreadWrapper{
		Type:     notifThreadWrapperType,
		Messages: []json.RawMessage{},
	})
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), string(fallback))

	// The fallback must still parse as a wrapper, because a caller stores it.
	wrapper, err := unwrapNotifContent(fallback)
	require.NoError(t, err)
	assert.Equal(t, notifThreadWrapperType, wrapper.Type)
	assert.Empty(t, wrapper.Messages)
}

func TestNotificationThreading_OrderedWorkerResolutionPreservesNativeAndWorkerSources(t *testing.T) {
	t.Parallel()
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	var queue agent.OutputQueue
	unavailable := true
	queue.Append(agent.OutputWrite{Label: "store unavailable", Run: func() error {
		if unavailable {
			return assert.AnError
		}
		return nil
	}})
	ordered := agent.NewOrderedProviderServices(&queue, sink)
	_, err := ordered.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system","subtype":"api_retry","attempt":1}`)})
	require.ErrorIs(t, err, assert.AnError)
	notice := map[string]interface{}{"type": "agent_status", "message": "Keep this notice"}
	ordered.PersistLeapMuxNotification(notice)
	notice["message"] = "changed after submission"
	_, err = ordered.PersistWorkerNotification(agent.MessageContent{Original: []byte(`{"type":"api_retry_resolved"}`)})
	require.ErrorIs(t, err, assert.AnError)
	require.ErrorIs(t, ordered.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Answer"}]}}`)}, agent.SpanInfo{}), assert.AnError)
	assert.Empty(t, listRows())
	unavailable = false
	require.NoError(t, queue.Flush())
	rows := listRows()
	require.Len(t, rows, 3)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[0].Source)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[1].Source)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[2].Source)
	retry := decodeNotifWrapper(t, rows[1].Content, rows[1].ContentCompression)
	require.Len(t, retry.Messages, 1)
	assert.JSONEq(t, `{"type":"api_retry_resolved"}`, string(retry.Messages[0]))
	worker := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, worker.Messages, 1)
	assert.JSONEq(t, `{"type":"agent_status","message":"Keep this notice"}`, string(worker.Messages[0]))
}
