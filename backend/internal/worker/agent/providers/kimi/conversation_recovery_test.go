package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationRecoveryReadsOlderPagesOutsideDispatchAndOrdersLiveEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v1/sessions/session_1/agents/main/turns", request.URL.Path)
		assert.Equal(t, "2", request.URL.Query().Get("before_ordinal"))
		assert.Equal(t, "200", request.URL.Query().Get("page_size"))
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"code":0,"data":{"has_more":false,"turns":[{"turn_id":"older","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"older:answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Older answer"}]}]}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := newKimiConversationRig(t)
	rig.agent.api = &kimiClient{endpoint: endpoint, timeout: 30 * time.Second}
	page := kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":true,"turns":[{"turn_id":"recent","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:01Z","steps":[],"items":[{"type":"assistant_message","item_id":"recent:answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Recent answer"}]}]}`)
	dispatched := make(chan struct{})
	go func() { rig.agent.HandleOutput(page); close(dispatched) }()
	select {
	case <-dispatched:
	case <-ctx.Done():
		t.Fatal("the older-page read held the native dispatcher")
	}
	require.True(t, rig.agent.PublishTurnActive().Active, "incomplete native history must hold input admission")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native older-page request did not start")
	}
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"live","ordinal":3,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:02Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"live","item":{"type":"assistant_message","item_id":"live:answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:02Z","text":"Live answer"}}`)
	assert.Empty(t, rig.sink.Messages(), "live rows wait behind the complete captured history")
	// Release through the same channel without closing it; cleanup owns the close.
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("the captured older-page read stopped before release")
	}
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 5 }, "all native history precedes the buffered live answer")
	var texts []string
	for _, row := range rig.sink.Messages() {
		view, err := decodeKimiConversationRecord(row.Content)
		require.NoError(t, err)
		if view.record.Type == contracts.KimiConversationRecordKindItem {
			texts = append(texts, *view.item.Text)
		}
	}
	assert.Equal(t, []string{"Older answer", "Recent answer", "Live answer"}, texts)
	assert.True(t, rig.agent.PublishTurnActive().Active, "the buffered running turn still owns activity")
}

func TestKimiConversationCompleteSnapshotPublishesReadyAfterReplacingRecovery(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turns","has_more":true,"turns":[{"turn_id":"recent","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
	active, found := rig.sink.LastTurnActive()
	require.True(t, found)
	require.True(t, active)
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[]}`)
	active, found = rig.sink.LastTurnActive()
	require.True(t, found)
	assert.False(t, active, "the complete replacement must publish the ready state")
	assert.Empty(t, rig.sink.Messages(), "the old page is absent from the complete replacement")
}

func TestKimiConversationAcceptedSubscriptionWaitsForItsSnapshot(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	accepted := rig.agent.beginInvocationRecovery(t.Context(), rig.sessionID())
	require.NotNil(t, accepted)
	accepted(true)
	rig.agent.dispatchMu.Lock()
	fenced := rig.agent.invocationRecovery != nil
	rig.agent.finishDispatch()
	require.True(t, fenced, "acceptance precedes native conversation state and cannot release invocation recovery")
	active, found := rig.sink.LastTurnActive()
	require.True(t, found)
	require.True(t, active)
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[]}`)
	rig.agent.dispatchMu.Lock()
	fenced = rig.agent.invocationRecovery != nil
	rig.agent.finishDispatch()
	assert.False(t, fenced)
	active, found = rig.sink.LastTurnActive()
	require.True(t, found)
	assert.False(t, active)
}

func TestKimiConversationOldSubscriptionCannotHoldAReplacementSession(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	accepted := rig.agent.beginInvocationRecovery(t.Context(), rig.sessionID())
	require.NotNil(t, accepted)
	rig.agent.Mu.Lock()
	rig.agent.sessionID = "replacement"
	rig.agent.Mu.Unlock()
	rig.agent.sink.UpdateSessionID("replacement")
	accepted(true)
	assert.False(t, rig.agent.PublishTurnActive().Active)
	rig.agent.dispatchMu.Lock()
	fenced := rig.agent.invocationRecovery != nil
	rig.agent.finishDispatch()
	assert.False(t, fenced)
}

func TestKimiConversationStartupWaitsForItsFirstSnapshot(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual"})
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	enter := sync.OnceFunc(func() { close(entered) })
	fake.beforeConversation = func(subscription fakeKapSubscribe, agentID string) {
		if subscription.SessionID == "session_stored" && agentID == kimiMainAgentID {
			enter()
			<-released
		}
	}
	rig := connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native initial page did not reach its controlled boundary")
	}
	active, found := rig.sink.LastTurnActive()
	require.True(t, found, "startup must publish its wait for the native conversation page")
	require.True(t, active)
	release()
	waitFor(t, func() bool { active, found := rig.sink.LastTurnActive(); return found && !active }, "the first complete native snapshot admits input")
}

func TestKimiConversationRecoveryReleasesFailedAttemptRetention(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("before_ordinal") == "1" {
			http.Error(w, "recoverable read failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"code":0,"data":{"has_more":true,"turns":[{"turn_id":"middle","ordinal":1,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := newKimiConversationRig(t)
	rig.agent.api = &kimiClient{endpoint: endpoint, timeout: 30 * time.Second}
	clock := testutil.NewQuartzMock(t)
	rig.agent.clock = clock
	timer := clock.Trap().NewTimer(kimiConversationRecoveryTimerTag)
	t.Cleanup(timer.Close)
	page := kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":true,"turns":[{"turn_id":"recent","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
	rig.agent.HandleOutput(page)
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), timer)
	rig.agent.dispatchMu.Lock()
	retained := rig.agent.pendingNativeBytes
	rig.agent.finishDispatch()
	assert.Equal(t, int64(len(page))+kimiPendingFrameOverhead, retained, "failed older pages must not consume quota across retries")
}

func conversationRecoveryTestTurn(id string, ordinal int64, text string) kimiConversationTurn {
	return kimiConversationTurn{kimiConversationTurnHeader: kimiConversationTurnHeader{
		TurnID: id, Ordinal: &ordinal, Kind: "agent", Status: contracts.KimiConversationStatusEnded,
		Outcome: contracts.KimiConversationOutcomeCompleted, StartedAt: "2026-11-18T00:00:00Z", Steps: []kimiConversationStep{},
	}, Items: []kimiConversationItem{{Type: contracts.KimiConversationItemKindAssistantMessage, ItemID: id + ":answer",
		Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
		CreatedAt: "2026-11-18T00:00:00Z", Text: &text}}}
}

func TestKimiConversationHistoryRejectsInvalidOlderPagesAndRetainsValidPages(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		page      any
		errorText string
		expected  int
	}{
		{"sparse older ordinal", map[string]any{"turns": []kimiConversationTurn{conversationRecoveryTestTurn("older", 0, "Older")}, "has_more": false}, "", 2},
		{"empty final page", map[string]any{"turns": []kimiConversationTurn{}, "has_more": false}, "", 1},
		{"missing turns", map[string]any{"has_more": false}, "complete turn page", 0},
		{"missing continuation", map[string]any{"turns": []kimiConversationTurn{}}, "complete turn page", 0},
		{"empty continuing page", map[string]any{"turns": []kimiConversationTurn{}, "has_more": true}, "complete turn page", 0},
		{"overlapping ordinal", map[string]any{"turns": []kimiConversationTurn{conversationRecoveryTestTurn("older", 2, "Overlap")}, "has_more": false}, "overlaps", 0},
		{"repeated identity", map[string]any{"turns": []kimiConversationTurn{conversationRecoveryTestTurn("recent", 0, "Another identity")}, "has_more": false}, "repeats a turn identity", 0},
		{"reversed ordinals", map[string]any{"turns": []kimiConversationTurn{conversationRecoveryTestTurn("one", 1, "One"), conversationRecoveryTestTurn("zero", 0, "Zero")}, "has_more": false}, "misorders", 0},
		{"large first turn", map[string]any{"turns": []kimiConversationTurn{conversationRecoveryTestTurn("large", 0, strings.Repeat("x", (1<<20)+64))}, "has_more": false}, "", 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/api/v1/sessions/session_1/agents/main/turns", request.URL.Path)
				assert.Equal(t, "2", request.URL.Query().Get("before_ordinal"))
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": scenario.page}))
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			rig := newKimiConversationRig(t)
			rig.agent.api = &kimiClient{endpoint: endpoint, timeout: 30 * time.Second}
			more := true
			latest := kimiConversationFrame{SessionID: "session_1", AgentID: "main", Turns: []kimiConversationTurn{conversationRecoveryTestTurn("recent", 2, "Recent")}, HasMore: &more}
			var retained int64
			turns, err := rig.agent.readConversationHistory(t.Context(), latest, func(size int64) error { retained += size; return nil })
			if scenario.errorText != "" {
				require.ErrorContains(t, err, scenario.errorText)
				assert.Nil(t, turns)
				return
			}
			require.NoError(t, err)
			require.Len(t, turns, scenario.expected)
			assert.Equal(t, "recent", turns[len(turns)-1].TurnID)
			assert.Positive(t, retained)
		})
	}
}

func TestKimiConversationHistoryPropagatesRetentionRefusal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"turns": []kimiConversationTurn{conversationRecoveryTestTurn("older", 0, "Older")}, "has_more": false,
		}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := newKimiConversationRig(t)
	rig.agent.api = &kimiClient{endpoint: endpoint, timeout: 30 * time.Second}
	more := true
	latest := kimiConversationFrame{SessionID: "session_1", AgentID: "main", Turns: []kimiConversationTurn{conversationRecoveryTestTurn("recent", 2, "Recent")}, HasMore: &more}
	refused := errors.New("the test cannot retain another native page")
	turns, err := rig.agent.readConversationHistory(t.Context(), latest, func(int64) error { return refused })
	require.ErrorIs(t, err, refused)
	assert.Nil(t, turns)
}

func TestKimiConversationMissingLiveBaseHoldsAdmissionUntilReplacement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		running bool
		gap     string
	}{
		{"missing turn", false, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"unknown prefix"}}`},
		{"missing item", true, `{"type":"item.delta","turn_id":"t0","item_id":"answer","field":"text","delta":"unknown suffix"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turns","turns":[],"has_more":false}`)
			if tc.running {
				rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
				rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":"prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":["turn-prompt"],"text":"Give one answer.","placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`)
				assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
			} else {
				assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
			}
			rig.conversationFrame(t, tc.gap)
			run := rig.agent.run(kimiMainAgentID)
			require.NotNil(t, run.conversationRecovery.Load())
			assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive())
			rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"assistant_message","item_id":"answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Not read from a complete base"}}`)
			assert.Empty(t, rig.sink.Messages())
			rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Authoritative snapshot"}]}]}`)
			awaitKimiTestOutput(t, rig.agent.sink)
			require.Nil(t, run.conversationRecovery.Load())
			assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
			rows := rig.sink.Messages()
			require.Len(t, rows, 2)
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			require.NotNil(t, view.item.Text)
			assert.Equal(t, "Authoritative snapshot", *view.item.Text)
			assert.True(t, rows[1].TurnEnd)
		})
	}
}

func TestKimiConversationMissingLiveBaseRenewsTheNativeSubscription(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	sessionID := rig.sink.LastSessionID()
	require.NotEmpty(t, sessionID)
	oldCount := len(rig.fake.subscribeFrames())
	require.Positive(t, oldCount)
	stream := rig.agent.stream
	stream.mu.Lock()
	original := stream.conn
	stream.mu.Unlock()
	require.NotNil(t, original)
	rig.fake.push(t, map[string]any{
		"type": "item.delta", "session_id": sessionID, "agent_id": "main", "event_created_at": "2026-11-18T00:00:00Z",
		"turn_id": "missing-turn", "item_id": "missing-item", "field": "text", "delta": "Unanchored suffix",
	})
	waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) > oldCount }, "the gap renews the native conversation subscription")
	subscription := rig.fake.subscribeFrames()[oldCount]
	assert.Equal(t, sessionID, subscription.SessionID)
	assert.Equal(t, []string{"main"}, subscription.AgentIDs)
	assertKimiNativeSubscriptionCommand(t, subscription)
	stream.mu.Lock()
	replacement := stream.conn
	stream.mu.Unlock()
	require.NotNil(t, replacement)
	assert.NotSame(t, original, replacement)
	main := rig.agent.run(kimiMainAgentID)
	waitFor(t, func() bool { return main.conversationRecovery.Load() == nil }, "the replacement snapshot releases input admission")
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
	assert.Empty(t, rig.sink.Messages())
}

func TestKimiPublicRecoveryDropsTheSupersededPrefixAtTheFrameLimit(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	run := rig.agent.run(kimiMainAgentID)
	rig.agent.dispatchMu.Lock()
	defer rig.agent.finishDispatch()
	recovery := rig.agent.awaitConversationSnapshot(run)
	frame := kimiConversationFrame{Type: "turn.updated", SessionID: "session_1", AgentID: "main", TurnID: "t0"}
	for range kimiMaxPendingEvents {
		rig.agent.retainConversationRecoveryFrame(run, recovery, frame, 256)
	}
	require.Len(t, recovery.frames, kimiMaxPendingEvents)
	require.EqualValues(t, kimiMaxPendingEvents*(256+kimiPendingFrameOverhead), rig.agent.pendingNativeBytes)
	rig.agent.retainConversationRecoveryFrame(run, recovery, frame, 256)
	assert.True(t, recovery.invalidated)
	assert.ErrorIs(t, recovery.ctx.Err(), context.Canceled)
	assert.Empty(t, recovery.frames)
	assert.Zero(t, recovery.bytes)
	assert.Zero(t, rig.agent.pendingNativeBytes)
	assert.Same(t, recovery, run.conversationRecovery.Load(), "admission stays held for the authoritative replacement page")
}

func TestKimiPublicRecoveryChargesTheOriginalWireBytesAndTheWholeSession(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.dispatchMu.Lock()
	defer rig.agent.finishDispatch()
	main := rig.agent.run(kimiMainAgentID)
	recovery := rig.agent.awaitConversationSnapshot(main)
	rig.agent.retainConversationRecoveryFrame(main, recovery, kimiConversationFrame{Type: "turn.updated", SessionID: "session_1", AgentID: "main", TurnID: "t0"}, kimiStreamReadLimit)
	require.True(t, recovery.invalidated, "a small projected frame still represents its original wire bytes")
	require.Empty(t, recovery.frames)
	require.Zero(t, rig.agent.pendingNativeBytes)
	rig.agent.replaceConversationRecovery(main, nil)

	children := make([]*kimiRun, 6)
	for i := range children {
		id := fmt.Sprintf("agent-%d", i)
		children[i] = rig.agent.run(id)
		recovery := rig.agent.awaitConversationSnapshot(children[i])
		frame := kimiConversationFrame{Type: "turn.updated", SessionID: "session_1", AgentID: id, TurnID: "t0"}
		for range 3 {
			rig.agent.retainConversationRecoveryFrame(children[i], recovery, frame, 4<<20)
		}
		if i < 5 {
			require.Len(t, recovery.frames, 3)
			assert.False(t, recovery.invalidated)
		} else {
			assert.True(t, recovery.invalidated)
			assert.Empty(t, recovery.frames)
			assert.Zero(t, recovery.bytes)
		}
	}
	assert.EqualValues(t, (60<<20)+15*kimiPendingFrameOverhead, rig.agent.pendingNativeBytes)
	for _, child := range children {
		current := child.conversationRecovery.Load()
		rig.agent.replaceConversationRecovery(child, nil)
		assert.ErrorIs(t, current.ctx.Err(), context.Canceled)
		assert.Zero(t, current.bytes)
		assert.Empty(t, current.frames)
	}
	assert.Zero(t, rig.agent.pendingNativeBytes)
}

func TestKimiConversationChildHistoryRetriesWithoutLosingItsOwnLiveItems(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	const nativeChild = "agent-0"
	childID := adoptNativeKimiChild(t, rig.agent, nativeChild)
	child := rig.sink.Child(childID)
	run := rig.agent.run(nativeChild)
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	rig.agent.clock = clock
	timer := clock.Trap().NewTimer(kimiConversationRecoveryTimerTag)
	t.Cleanup(timer.Close)
	var requests atomic.Int64
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer "+fakeKapToken, request.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/sessions/session_1/agents/agent-0/turns", request.URL.Path)
		assert.Equal(t, "2", request.URL.Query().Get("before_ordinal"))
		if requests.Add(1) == 1 {
			http.Error(w, "history unavailable", http.StatusServiceUnavailable)
			return
		}
		writeFakeKapEnvelope(w, http.StatusOK, 0, "success", map[string]any{
			"turns":    []kimiConversationTurn{conversationRecoveryTestTurn("older", 0, "Older child answer")},
			"has_more": false,
		})
	})
	rig.conversationAgentFrame(t, nativeChild, `{"type":"turns","has_more":true,"turns":[{"turn_id":"recent","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
	delay := testutil.WaitForTimer(t, ctx, timer)
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.startPublicTurn(t, nativeChild, "live", 3)
	rig.conversationAgentFrame(t, nativeChild, `{"type":"item.ended","turn_id":"live","item":{"type":"assistant_message","item_id":"live-answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Live child answer"}}`)
	assert.Empty(t, child.Messages())
	clock.Advance(delay).MustWait(ctx)
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the exact child history retry finishes")
	require.Nil(t, run.conversationRecovery.Load())
	require.NoError(t, agent.FlushPendingOutput(run.childLifetime.writer))
	rows := child.Messages()
	require.Len(t, rows, 4)
	var answers []string
	for _, row := range rows {
		view, err := decodeKimiConversationRecord(row.Content)
		require.NoError(t, err)
		assert.Equal(t, nativeChild, view.record.AgentID)
		if view.item != nil {
			require.NotNil(t, view.item.Text)
			answers = append(answers, *view.item.Text)
		}
	}
	assert.Equal(t, []string{"Older child answer", "Live child answer"}, answers)
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	assert.Empty(t, rig.sink.Messages())
	assert.EqualValues(t, 2, requests.Load())
	rig.agent.dispatchMu.Lock()
	retained := rig.agent.pendingNativeBytes
	rig.agent.finishDispatch()
	assert.Zero(t, retained)
}

func TestKimiConversationRecoveryCancelsItsReplacedRequest(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	ctx := testutil.DeadlineContext(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v1/sessions/session_1/agents/main/turns", request.URL.Path)
		close(entered)
		select {
		case <-request.Context().Done():
			close(canceled)
		case <-ctx.Done():
		}
	})
	rig.agent.api.timeout = 30 * time.Second
	rig.conversationFrame(t, `{"type":"turns","has_more":true,"turns":[{"turn_id":"stale","ordinal":2,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"old","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Stale page"}]}]}`)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the older page request did not start")
	}
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"replacement","ordinal":3,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{"type":"assistant_message","item_id":"retained","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Replacement page"}]}]}`)
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("the complete replacement did not cancel the old page request")
	}
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the displaced page request leaves recovery")
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.NotNil(t, view.item.Text)
	assert.Equal(t, "replacement", view.record.TurnID)
	assert.Equal(t, "Replacement page", *view.item.Text)
	assert.True(t, rows[1].TurnEnd)
	assert.False(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiConnectedRigCancelsBeforeJoiningRecoveryWorkers(t *testing.T) {
	t.Parallel()
	finished := make(chan struct{})
	t.Run("cleanup", func(t *testing.T) {
		rig := newKimiTestRig(t, agent.Options{})
		ctx := testutil.DeadlineContext(t)
		entered := make(chan struct{})
		rig.agent.transcriptRecoveryWork.Run("cleanup-probe", func() {
			close(entered)
			<-rig.agent.Context().Done()
			close(finished)
		})
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("the recovery worker did not start")
		}
		t.Cleanup(func() {
			assert.ErrorIs(t, rig.agent.Context().Err(), context.Canceled, "the rig must cancel its context before cleanup joins recovery workers")
			// Explicit cancellation also releases the worker when the assertion fails.
			rig.agent.CancelForTest()
		})
	})
	select {
	case <-finished:
	default:
		t.Fatal("the rig returned before its recovery worker finished")
	}
}
