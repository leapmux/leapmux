package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestKimiCreateSession(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{WorkingDir: "/work/app"})

	creates := rig.fake.requestsTo("POST " + kimiRouteSessions)
	require.Len(t, creates, 1)
	assert.JSONEq(t, `{"metadata":{"cwd":"/work/app"}}`, string(creates[0].Body))
	assert.Equal(t, "session_1", rig.sessionID())

	routes := rig.fake.routes()
	create, profile := -1, -1
	for i, route := range routes {
		switch route {
		case "POST " + kimiRouteSessions:
			create = i
		case "POST " + kimiSessionPath("session_1", "/profile"):
			if profile < 0 {
				profile = i
			}
		}
	}
	assert.Less(t, create, profile, "the new session gets its profile before anything uses it")
	subscribes := rig.fake.subscribeFrames()
	require.Len(t, subscribes, 1)
	assert.Equal(t, "session_1", subscribes[0].SessionID)
}

func TestKimiRuntimeExitEndsRootAndChildCompaction(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	childID := adoptNativeKimiChild(t, rig.agent, "agent-1")
	for _, id := range []string{kimiMainAgentID, "agent-1"} {
		rig.agent.HandleOutput(kimiCompactingStateFrame(t, id, true))
	}
	rig.agent.settleAfterExit(agent.MessageCompletionInterrupted)
	value, exists := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
	value, exists = rig.sink.Child(childID).LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
}

func TestKimiLiveWarningPersistsOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for range 2 {
		rig.feed(t, map[string]any{"type": "warning", "code": "agents-md-oversized", "message": "AGENTS.md total exceeds the recommended size."})
	}
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}

func TestKimiSessionWarningsPersistOncePerSession(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	for _, sessionID := range []string{"session_1", "session_2"} {
		rig.fake.reply("GET "+kimiSessionPath(sessionID, "/warnings"), fakeKapReply{Data: map[string]any{
			"warnings": []map[string]string{{"code": "agents-md-oversized", "message": "A profile warning.", "severity": "warning"}},
		}})
		rig.agent.Mu.Lock()
		rig.agent.sessionID = sessionID
		rig.agent.Mu.Unlock()
		rig.sink.UpdateSessionID(sessionID)
		rig.agent.persistSessionWarnings(t.Context(), sessionID)
		rig.agent.persistSessionWarnings(t.Context(), sessionID)
	}
	rows := rig.sink.PersistedNotifications()
	require.Len(t, rows, 2)
	assert.Equal(t, "session_1", rows[0].AgentSessionID)
	assert.Equal(t, "session_2", rows[1].AgentSessionID)
}

func TestKimiLiveWarningSuppressesTheReadCopy(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.sink.UpdateSessionID(rig.sessionID())
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/warnings"), fakeKapReply{Data: map[string]any{
		"warnings": []map[string]string{{"code": "agents-md-oversized", "message": "A profile warning."}},
	}})
	rig.feed(t, map[string]any{"type": "warning", "code": "agents-md-oversized", "message": "A profile warning."})
	rig.agent.persistSessionWarnings(t.Context(), rig.sessionID())
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}

func TestKimiSessionWarningsIgnoreInvalidEntriesAndReadFailures(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	path := "GET " + kimiSessionPath(rig.sessionID(), "/warnings")
	rig.fake.reply(path, fakeKapReply{Code: 50000, Msg: "warnings unavailable"})
	rig.agent.persistSessionWarnings(t.Context(), rig.sessionID())
	assert.Empty(t, rig.sink.PersistedNotifications())
	rig.fake.reply(path, fakeKapReply{Data: map[string]any{"warnings": []any{
		nil, 7, map[string]any{"message": 7}, map[string]any{"message": " "},
		map[string]any{"code": "native-warning", "message": "A valid warning."},
	}}})
	rig.agent.persistSessionWarnings(t.Context(), rig.sessionID())
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Contains(t, string(rig.sink.PersistedNotifications()[0].Content), "A valid warning.")
}

func TestKimiCreateSessionRefusesAnIDTheServerWouldNotIssue(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.reply("POST "+kimiRouteSessions, fakeKapReply{Data: map[string]any{"id": "../other"}})
	_, err := rig.agent.createSession(rig.agent.Context(), kimiSettings{model: "kimi-k2"})
	require.ErrorContains(t, err, "session id")
}

func TestKimiResumeSession(t *testing.T) {
	t.Parallel()

	t.Run("reads the stored session, subscribes, and writes only what differs", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-text", Thinking: "", Permission: "yolo"})
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{
			ResumeSessionID: "session_stored",
			Options:         options(agent.OptionIDPermissionMode, contracts.KimiModeAuto),
		})
		assert.Equal(t, "session_stored", rig.sessionID())
		assert.Empty(t, fake.requestsTo("POST "+kimiRouteSessions), "a resume creates no session")

		assert.Len(t, fake.subscribeFrames(), 1, "the native subscription resumes the stored session")
		assert.Empty(t, fake.requestsTo("GET "+kimiSessionPath("session_stored", "/status")))

		profiles := fake.requestsTo("POST " + kimiSessionPath("session_stored", "/profile"))
		require.Len(t, profiles, 1)
		assert.JSONEq(t, `{"agent_config":{"permission_mode":"auto"}}`, string(profiles[0].Body),
			"the stored session keeps its model; only the axis the launch states differently is written")
		current := agent.CurrentOptions(rig.agent.OptionGroups())
		assert.Equal(t, "kimi-text", current[agent.OptionIDModel])
		assert.Equal(t, contracts.KimiModeAuto, current[agent.OptionIDPermissionMode])
	})

	t.Run("a stored session the launch agrees with gets no write", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-text", Permission: "manual"})
		connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored"})
		assert.Empty(t, fake.requestsTo("POST "+kimiSessionPath("session_stored", "/profile")))
	})

	// The server keeps the thinking level of a stored session across a model switch. A
	// stopped agent that switched its model while it was offline resumes on Auto, and the
	// level of the old model must not reach the new one.
	t.Run("a resumed session on Auto states the level that Auto stands for when the model moves", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name   string
			stored fakeKapSession
			target string
			want   string
		}{
			{
				name:   "a model that cannot think takes no level",
				stored: fakeKapSession{Model: "kimi-k2", Thinking: "high", Permission: "manual"},
				target: "kimi-text", want: "off",
			},
			{
				name:   "a model that thinks takes the configured level",
				stored: fakeKapSession{Model: "kimi-text", Thinking: "off", Permission: "manual"},
				target: "kimi-k2", want: "medium",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				fake, server := newFakeKap(t)
				fake.store("session_stored", tc.stored)
				connectKimiTestRig(t, fake, server.URL, agent.Options{
					ResumeSessionID: "session_stored",
					Options:         options(agent.OptionIDModel, tc.target),
				})
				profiles := fake.requestsTo("POST " + kimiSessionPath("session_stored", "/profile"))
				require.Len(t, profiles, 1)
				assert.JSONEq(t, `{"agent_config":{"model":"`+tc.target+`","thinking":"`+tc.want+`"}}`, string(profiles[0].Body))
				session, ok := fake.session("session_stored")
				require.True(t, ok)
				assert.Equal(t, tc.want, session.Thinking, "the level of the old model does not survive the resume")
			})
		}
	})

	t.Run("a resumed session on the same model keeps its level", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Thinking: "high", Permission: "manual"})
		connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored", Options: options(agent.OptionIDModel, "kimi-k2")})
		assert.Empty(t, fake.requestsTo("POST "+kimiSessionPath("session_stored", "/profile")), "no model moved, so nothing needs restating")
	})

	t.Run("a resumed session whose launch states a level moves only that level", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Thinking: "high", Permission: "manual"})
		connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored", Options: options(agent.OptionIDEffort, "low")})
		profiles := fake.requestsTo("POST " + kimiSessionPath("session_stored", "/profile"))
		require.Len(t, profiles, 1)
		assert.JSONEq(t, `{"agent_config":{"thinking":"low"}}`, string(profiles[0].Body))
	})

	t.Run("a model the catalog lacks is not written", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-text", Permission: "manual"})
		connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored", Options: options(agent.OptionIDModel, "gone")})
		assert.Empty(t, fake.requestsTo("POST "+kimiSessionPath("session_stored", "/profile")))
	})

	t.Run("a session the server does not know fails the resume", func(t *testing.T) {
		t.Parallel()
		_, server := newFakeKap(t)
		a, _, opts := newConnectedKimiAgent(t, server.URL, agent.Options{ResumeSessionID: "session_gone"})
		err := a.openStartupSession(opts, 30*time.Second)
		require.ErrorIs(t, err, errKimiSessionUnavailable)
		a.Mu.Lock()
		sessionID := a.sessionID
		a.Mu.Unlock()
		assert.Empty(t, sessionID, "a failed resume leaves no session behind")
	})

	t.Run("a stored session the server cannot subscribe fails the resume", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-k2", Permission: "manual"})
		fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeSessionNotFound })
		a, _, opts := newConnectedKimiAgent(t, server.URL, agent.Options{ResumeSessionID: "session_stored"})
		require.ErrorIs(t, a.openStartupSession(opts, 30*time.Second), errKimiSessionUnavailable)
	})

	t.Run("an unsafe session id fails before any request", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		_, err := rig.agent.resumeSession(rig.agent.Context(), "../x", kimiSettings{})
		require.ErrorContains(t, err, "session id")
	})
}

func TestKimiFreshStartupRefusesUnusableSession(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"create", "profile", "subscribe"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fake, server := newFakeKap(t)
			a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{})
			want := ""
			switch stage {
			case "create":
				fake.reply("POST "+kimiRouteSessions, fakeKapReply{Code: 50000, Msg: "disk full"})
				want = "disk full"
			case "profile":
				fake.reply("POST "+kimiSessionPath("session_1", "/profile"), fakeKapReply{Code: 50000, Msg: "profile locked"})
				want = "profile locked"
			case "subscribe":
				fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeSessionNotFound })
				want = "subscribe to the Kimi Code session"
			}
			err := a.openStartupSession(opts, 30*time.Second)
			require.ErrorContains(t, err, want)
			require.Len(t, fake.requestsTo("POST "+kimiRouteSessions), 1, "the regression must actually attempt fresh startup")
			a.Mu.Lock()
			assert.Empty(t, a.sessionID)
			a.Mu.Unlock()
			assert.Empty(t, sink.MessageSessionID())
			assert.Empty(t, sink.Goals())
			a.dispatchMu.Lock()
			assert.Nil(t, a.pendingSession)
			a.dispatchMu.Unlock()
		})
	}
}

func TestKimiCompactContext(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	require.NoError(t, rig.agent.CompactContext())
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionCompact)), 1)

	rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionCompact), fakeKapReply{Code: 40903, Msg: "nothing to compact"})
	require.ErrorContains(t, rig.agent.CompactContext(), "nothing to compact")
}

// A native resubscription supplies current flat state before its acknowledgement.
// Mode changes during a disconnect therefore need no removed status or snapshot route.
func TestKimiReconnectReadsBackTheStatusTheStreamMissed(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.update("session_1", func(session *fakeKapSession) {
		session.PlanMode = true
		session.SwarmMode = true
	})
	require.NoError(t, rig.fake.disconnectClients())

	waitFor(t, func() bool {
		return rig.sink.PermissionMode() == contracts.KimiModePlan &&
			rig.sink.SettingsRefreshCount() > 0 && rig.sink.LastSettingsRefresh().Options[kimiOptionSwarmMode] == kimiSwarmOn
	}, "the reconnect reports the plan mode and the swarm mode the stream missed")
	current := agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, contracts.KimiModePlan, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOn, current[kimiOptionSwarmMode])
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

func TestKimiNativeReconnectCompletionRequiresConversationDispatch(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	ctx := testutil.DeadlineContext(t)
	pageHeld := make(chan struct{})
	releasePage := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePage) })
	defer release()
	var hold sync.Once
	rig.fake.mu.Lock()
	rig.fake.beforeConversation = func(sub fakeKapSubscribe, agentID string) {
		if sub.SessionID != rig.sessionID() || agentID != kimiMainAgentID {
			return
		}
		hold.Do(func() {
			close(pageHeld)
			select {
			case <-releasePage:
			case <-ctx.Done():
			}
		})
	}
	rig.fake.mu.Unlock()
	finished := beginNativeKimiReconnect(t, rig)
	select {
	case <-pageHeld:
	case <-ctx.Done():
		t.Fatal("the reconnect did not reach its initial conversation page")
	}
	assert.False(t, finished(), "the subscribe response cannot prove conversation dispatch")
	release()
	waitFor(t, finished, "the native initial page reaches dispatch without an obsolete sequence marker")
	waitFor(t, func() bool {
		return rig.agent.run(kimiMainAgentID).conversationRecovery.Load() == nil
	}, "native conversation recovery completes")
}

// beginNativeKimiReconnect supplies native replay frames and returns their dispatch completion check.
func beginNativeKimiReconnect(t *testing.T, rig *kimiTestRig, frames ...any) func() bool {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	sessionID := rig.sessionID()
	replaying := make(chan struct{})
	requestDone := make(chan struct{})
	requestResult := make(chan error, 1)
	var dispatched atomic.Bool
	t.Cleanup(func() {
		cancel()
		<-requestDone
	})
	go func() {
		defer close(requestDone)
		select {
		case <-replaying:
		case <-ctx.Done():
			return
		}
		// The fake reads this command after writing the initial replay. Its response
		// barrier therefore follows every frame of that replay in the dispatcher.
		err := rig.agent.stream.sendSessionSubscribe(ctx, sessionID, func(frame kimiFrame) {
			if frame.Type == kimiFrameTaskResponse && frame.Code == kimiCodeOK {
				rig.agent.stream.queueResponseBarrier(func() { dispatched.Store(true) })
			}
		})
		requestResult <- err
	}()
	rig.fake.mu.Lock()
	previous := rig.fake.beforeReplay
	var started sync.Once
	rig.fake.beforeReplay = func(sub fakeKapSubscribe) {
		if previous != nil {
			previous(sub)
		}
		if sub.SessionID == sessionID {
			started.Do(func() {
				rig.fake.mu.Lock()
				rig.fake.beforeReplay = previous
				rig.fake.mu.Unlock()
				close(replaying)
			})
		}
	}
	rig.fake.mu.Unlock()
	rig.fake.update(sessionID, func(session *fakeKapSession) { session.ReplayFrames = append([]any(nil), frames...) })
	rig.fake.setSubscribeCodeFor(nil)
	require.NoError(t, rig.fake.disconnectClients())
	return func() bool {
		select {
		case err := <-requestResult:
			require.NoError(t, err, "the native replay barrier receives its own response")
		default:
		}
		return dispatched.Load() && !agent.OutputQueueFor(rig.agent.sink).HasPending()
	}
}

// Native resubscription restores transcript state and re-emits live controls.
func TestKimiResyncRestoresWhatTheStreamCouldNotReplay(t *testing.T) {
	t.Parallel()
	t.Run("publishes the requests raised during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		approval := approvalEvent("approval_9", map[string]any{"kind": "command", "command": "ls"})
		approval["session_id"], approval["tool_call_id"] = "session_1", "call_9"
		question := questionEvent("question_9")
		question["session_id"], question["agent_id"] = "session_1", "agent-0"
		finished := beginNativeKimiReconnect(t, rig, approval, question)
		waitFor(t, finished, "the native pending interactions complete replay")
		require.Equal(t, 2, rig.sink.PublishedControlCount(), "session-wide replay includes a child's control without its conversation subscription")
		byID := map[string]map[string]any{}
		for _, control := range rig.sink.PublishedControls() {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(control.Payload, &payload))
			byID[control.RequestID] = payload
		}
		assert.Equal(t, contracts.KimiFrameInteraction, byID["approval_9"]["type"])
		assert.Equal(t, kimiMainAgentID, byID["approval_9"]["agent_id"])
		assert.Equal(t, "session_1", byID["approval_9"]["session_id"])
		assert.Equal(t, contracts.KimiFrameInteraction, byID["question_9"]["type"])
		assert.Equal(t, "agent-0", byID["question_9"]["agent_id"])
		require.NoError(t, rig.agent.SendRawInput(agent.RawInput{Data: answerFor(t, rig, "approval_9", "allow", nil, nil)}))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "interactions", "approval_9", "")), 1)
	})
	t.Run("withdraws only controls absent from the confirmed native replay", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.feed(t, approvalEvent("approval_2", map[string]any{"kind": "command", "command": "pwd"}))
		rig.agent.Mu.Lock()
		previous := rig.agent.controls["approval_2"]
		rig.agent.Mu.Unlock()
		kept := approvalEvent("approval_2", map[string]any{"kind": "command", "command": "pwd"})
		kept["session_id"] = rig.sessionID()
		finished := beginNativeKimiReconnect(t, rig, kept)
		waitFor(t, finished, "the native reconnect replays only its pending interaction")
		waitFor(t, func() bool { return len(rig.sink.CanceledControls()) == 1 }, "the confirmed replay withdraws the resolved control")
		assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
		assert.Equal(t, 2, rig.sink.PublishedControlCount(), "a replayed pending request keeps its claim without a duplicate publication")
		rig.agent.Mu.Lock()
		assert.Len(t, rig.agent.controls, 1, "replay leaves one active card rather than a duplicate")
		assert.Same(t, previous, rig.agent.controls["approval_2"])
		_, stale := rig.agent.controls["approval_1"]
		rig.agent.Mu.Unlock()
		assert.False(t, stale)
		require.ErrorIs(t, rig.agent.SendRawInput(agent.RawInput{Data: answerFor(t, rig, "approval_1", "allow", nil, nil)}), errKimiControlGone)
		require.NoError(t, rig.agent.SendRawInput(agent.RawInput{Data: answerFor(t, rig, "approval_2", "allow", nil, nil)}))
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "interactions", "approval_1", "")))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "interactions", "approval_2", "")), 1)
	})
	t.Run("clears an ended main turn while background work keeps the session busy", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const tool = `{"type":"tool_call","item_id":"call_1","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"executing","input":{"command":"run the held command"}}`
		native.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":`+tool+`}`)
		rig.fake.update("session_1", func(session *fakeKapSession) { session.Busy = true })
		page := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+tool+`]}]}`))
		finished := beginNativeKimiReconnect(t, rig,
			json.RawMessage(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"background","kind":"shell","status":"running","detached":true,"description":"Background work"}`), page)
		waitFor(t, finished, "the native ended turn and background state complete replay")
		last, _ := rig.sink.LastTurnActive()
		assert.False(t, last, "the native interrupted turn releases the main input queue")
		var closing *agenttest.Message
		for _, message := range rig.sink.Messages() {
			if message.Closing {
				copy := message
				closing = &copy
			}
		}
		require.NotNil(t, closing)
		assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
		view, err := decodeKimiConversationRecord(closing.Content)
		require.NoError(t, err)
		require.NotNil(t, view.item)
		assert.Equal(t, "call_1", view.item.ItemID)
		assert.Equal(t, "running", view.item.Status, "interruption does not fabricate an ended native tool")
		messages := rig.sink.Messages()
		require.NotEmpty(t, messages)
		assert.True(t, messages[len(messages)-1].TurnEnd)
		rig.fake.setBusy("session_1", false)
		require.NoError(t, rig.agent.SendInput("Next.", nil))
	})
	t.Run("starts a main turn that started during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.update("session_1", func(session *fakeKapSession) { session.Busy = true })
		page := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t3","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`))
		finished := beginNativeKimiReconnect(t, rig, page)
		waitFor(t, finished, "the recovered native running turn completes replay")
		assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(), "a turn without a steerable user item takes no steer")
		var busy *agent.AgentBusyError
		require.ErrorAs(t, rig.agent.SendInput("Next.", nil), &busy)
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t3","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		last, _ := rig.sink.LastTurnActive()
		assert.False(t, last)
	})
	t.Run("keeps a main user turn that still runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		const prompt = `{"type":"user_message","item_id":"prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Keep working.","prompt_ids":[],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}`
		native.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+prompt+`}`)
		rig.fake.update("session_1", func(session *fakeKapSession) { session.Busy = true })
		page := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+prompt+`]}]}`))
		finished := beginNativeKimiReconnect(t, rig, page)
		waitFor(t, finished, "the native page applies its authoritative running header and user item")
		assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
	})
	t.Run("loads and re-subscribes a session the server dropped", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		var dropped sync.Once
		rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
			first := false
			dropped.Do(func() { first = true })
			if first {
				return kimiCodeSessionNotFound
			}
			for _, state := range rig.fake.sessionStateFrames(sub.SessionID) {
				rig.fake.push(t, state)
			}
			approval := approvalEvent("approval_9", map[string]any{"kind": "command", "command": "ls"})
			approval["session_id"] = sub.SessionID
			rig.fake.push(t, approval)
			return kimiCodeOK
		})
		require.NoError(t, rig.fake.disconnectClients())
		waitFor(t, func() bool { return rig.sink.PublishedControlCount() == 1 }, "the loaded native session re-emits its pending request")
		require.Len(t, rig.fake.subscribeFrames(), 3)
		rig.agent.stream.mu.Lock()
		_, tracked := rig.agent.stream.sessions["session_1"]
		rig.agent.stream.mu.Unlock()
		assert.True(t, tracked)
	})
}

// kimiRecoveryChild supplies native task and transcript evidence across a reconnect.
type kimiRecoveryChild struct {
	agentID, callID, taskID string
	background              bool
	omitTaskReference       bool
}

func (child kimiRecoveryChild) taskState(status, outcome string) map[string]any {
	state := map[string]any{"task_id": child.taskID, "kind": "subagent", "status": status,
		"detached": child.background, "child_agent_id": child.agentID, "parent_tool_call_id": child.callID, "description": "Reviewer"}
	if outcome != "" {
		state["outcome"] = outcome
	}
	return state
}

const kimiRecoveryNativeParent = `{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}`
const kimiRecoveryNativeAnswer = `{"type":"assistant_message","item_id":"answer","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"Found it."}`

func (child kimiRecoveryChild) nativeInvocation(result string) map[string]any {
	ref := map[string]any{"agent_id": child.agentID, "role": "child"}
	if child.taskID != "" && !child.omitTaskReference {
		ref["task_id"] = child.taskID
	}
	item := map[string]any{"type": "tool_call", "item_id": child.callID, "name": "Agent", "status": "running",
		"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z", "step_index": 1,
		"stage": "executing", "agent_refs": []any{ref},
		"input": map[string]any{"description": "Reviewer", "prompt": "Review the diff.", "run_in_background": child.background}}
	if result != "" {
		item["status"], item["outcome"], item["output"] = "ended", "completed", result
		delete(item, "stage")
	}
	return item
}

func kimiRecoveryNativeParentPage(t *testing.T, rig *kimiTestRig, items ...map[string]any) json.RawMessage {
	t.Helper()
	var turn map[string]any
	require.NoError(t, json.Unmarshal([]byte(kimiRecoveryNativeParent), &turn))
	turn["items"] = items
	raw, err := json.Marshal(turn)
	require.NoError(t, err)
	return kimiConversationTestPage(t, rig.sessionID(), "main", raw)
}

func (child kimiRecoveryChild) startNative(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.conversationFrame(t, `{"type":"turn.started","turn":`+kimiRecoveryNativeParent+`}`)
	item, err := json.Marshal(child.nativeInvocation(""))
	require.NoError(t, err)
	native.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":`+string(item)+`}`)
	task := child.taskState("running", "")
	task["type"], task["session_id"], task["agent_id"] = contracts.KimiEventTask, rig.sessionID(), kimiMainAgentID
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	rig.agent.HandleOutput(raw)
	run := rig.agent.run(child.agentID)
	waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the child receives its initial native page")
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), child.agentID, `{"type":"turn.started","turn":`+kimiRecoveryNativeParent+`}`))
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), child.agentID, `{"type":"item.started","turn_id":"t0","item":`+kimiRecoveryNativeAnswer+`}`))
	awaitKimiTestOutput(t, rig.agent.sink)
}

func (child kimiRecoveryChild) nativePage(t *testing.T, rig *kimiTestRig, outcome, failure string) json.RawMessage {
	t.Helper()
	var turn map[string]any
	require.NoError(t, json.Unmarshal([]byte(kimiRecoveryNativeParent), &turn))
	turn["items"] = []json.RawMessage{json.RawMessage(kimiRecoveryNativeAnswer)}
	if outcome != "" {
		rig.agent.Mu.Lock()
		endedAt := rig.agent.attachedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
		rig.agent.Mu.Unlock()
		turn["status"], turn["outcome"], turn["ended_at"] = "ended", outcome, endedAt
		turn["steps"] = []any{map[string]any{"index": 1, "outcome": outcome,
			"started_at": "2026-11-18T00:00:00Z", "ended_at": endedAt}}
		if failure != "" {
			turn["error"] = map[string]any{"code": "model_error", "message": failure}
		}
	}
	raw, err := json.Marshal(turn)
	require.NoError(t, err)
	return kimiConversationTestPage(t, rig.sessionID(), child.agentID, raw)
}

func TestKimiResyncRestoresSubagentsAndTasks(t *testing.T) {
	t.Parallel()

	rowStatus := func(rig *kimiTestRig, rowKey string) func() bool {
		return func() bool {
			row, ok := rig.sink.BackgroundTask(rowKey)
			return ok && row.Status.IsFinished()
		}
	}

	for name, tc := range map[string]struct {
		turn, outcome, report, failure string
		status                         bgtask.Status
		completion                     agent.MessageCompletion
	}{
		"completed": {turn: "completed", outcome: "completed", report: "All good.", status: bgtask.StatusSucceeded, completion: agent.MessageCompletionComplete},
		"failed":    {turn: "failed", outcome: "failed", failure: "provider exploded", status: bgtask.StatusFailed, completion: agent.MessageCompletionError},
		"cancelled": {turn: "interrupted", outcome: "killed", status: bgtask.StatusStopped, completion: agent.MessageCompletionInterrupted},
	} {
		t.Run("a subagent that "+name+" during the gap closes with the status the server reports", func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			spec := kimiRecoveryChild{agentID: "agent-0", callID: "call_child", taskID: "task-child"}
			spec.startNative(t, rig)
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				assert.Equal(t, spec.taskID, command.TaskID)
				state := spec.taskState("ended", tc.outcome)
				if tc.failure != "" {
					state["error"] = tc.failure
				}
				return state
			}
			if tc.report != "" {
				rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
					return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": tc.report}}
				}
			}
			rig.fake.mu.Unlock()
			frames := []any{
				json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`),
				kimiRecoveryNativeParentPage(t, rig, spec.nativeInvocation("")),
				spec.nativePage(t, rig, tc.turn, tc.failure),
			}
			finished := beginNativeKimiReconnect(t, rig, frames...)
			waitFor(t, finished, "the native reconnect restores the child transcript")
			waitFor(t, rowStatus(rig, "session_1/agent-0"), "task.watch restores the missed outcome")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the task outcome finishes recovery")
			awaitKimiTestOutput(t, rig.agent.sink)
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			markerStatus := tc.status
			if tc.turn == "completed" {
				markerStatus = bgtask.StatusEndedWithUnknownOutcome
			}
			assert.Equal(t, bgtask.RoleChildActivity, row.Role)
			assert.Equal(t, markerStatus, row.Status)
			task, exists := rig.sink.BackgroundTask(kimiTaskRowKey(rig.sessionID(), "main", spec.taskID))
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
			assert.Equal(t, tc.status, task.Status)
			assert.Equal(t, agent.TurnState{}, rig.agent.ActiveChildTurnState(row.RowKey))
			var foundText, foundFailure bool
			for _, message := range child.Messages() {
				if view, err := decodeKimiConversationRecord(message.Content); err == nil && view.item != nil && view.item.ItemID == "answer" {
					require.NotNil(t, view.item.Text)
					assert.Equal(t, "Found it.", *view.item.Text)
					assert.Equal(t, tc.completion, message.Completion)
					foundText = true
				}
				if tc.failure != "" && eventType(t, message) == contracts.AssembledMessageType {
					_, diagnostic := assembledRow(t, message)
					if diagnostic == tc.failure {
						foundFailure = true
						assert.Equal(t, string(agent.MessageCompletionError), assembledCompletion(t, message))
						assert.True(t, message.TranscriptOnly)
					}
				}
			}
			assert.True(t, foundText, "the authoritative recovered text retains its native completion")
			if tc.failure != "" {
				assert.True(t, foundFailure, "the missed task failure reaches the child transcript")
			}
			if tc.report != "" {
				waitFor(t, func() bool { return len(child.LeapMuxNotifications()) == 1 }, "task output supplies the ordinary Agent report")
				assert.Contains(t, fmt.Sprint(child.LeapMuxNotifications()), tc.report)
			}
			before, reports := child.MessageCount(), len(child.LeapMuxNotifications())
			repeated := beginNativeKimiReconnect(t, rig, frames...)
			waitFor(t, repeated, "a repeated native replay completes")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "repeated task reconciliation finishes")
			assert.Equal(t, before, child.MessageCount())
			assert.Len(t, child.LeapMuxNotifications(), reports)
			last, _ := rig.sink.LastTurnActive()
			assert.True(t, last, "the main turn still runs")
		})
	}

	t.Run("a subagent that started and ended during the gap opens and closes", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.started","turn":`+kimiRecoveryNativeParent+`}`)
		result := "agent_id: agent-2\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nDone."
		child := kimiRecoveryChild{agentID: "agent-2", callID: "call_gone"}
		finished := beginNativeKimiReconnect(t, rig, kimiRecoveryNativeParentPage(t, rig, child.nativeInvocation(result)), child.nativePage(t, rig, "completed", ""))
		waitFor(t, finished, "the reconnect applies the parent result")
		waitFor(t, func() bool {
			for _, sub := range rig.fake.subscribeFrames() {
				if slices.Contains(sub.AgentIDs, child.agentID) && rig.agent.run(child.agentID).conversationRecovery.Load() == nil {
					return true
				}
			}
			return false
		}, "the newly discovered child completes its own subscription")
		awaitKimiTestOutput(t, rig.agent.sink)
		row, _ := childOf(t, rig.sink, "session_1/agent-2")
		assert.Equal(t, bgtask.RoleChildActivity, row.Role)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
		assert.Equal(t, bgtask.KindSubagent, row.Kind)
		identity := kimiInvocationIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", toolCallID: "call_gone"}
		outcome := invocationOutcomeOf(t, rig.sink, identity, "agent-2")
		assert.Equal(t, bgtask.StatusSucceeded, outcome.Status)
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
	})

	t.Run("recovers a foreground stop without ending a running background child", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		foreground := kimiRecoveryChild{agentID: "agent-0", callID: "call_child", taskID: "foreground-task"}
		background := kimiRecoveryChild{agentID: "agent-1", callID: "call_background", taskID: "background-task", background: true}
		foreground.startNative(t, rig)
		background.startNative(t, rig)
		_, foregroundSink := childOf(t, rig.sink, "session_1/agent-0")
		_, backgroundSink := childOf(t, rig.sink, "session_1/agent-1")
		foregroundBefore, backgroundBefore := len(foregroundSink.TurnActives()), len(backgroundSink.TurnActives())
		rig.fake.mu.Lock()
		rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
			switch command.TaskID {
			case foreground.taskID:
				return foreground.taskState("ended", "killed")
			case background.taskID:
				return background.taskState("running", "")
			default:
				t.Errorf("unexpected task lookup for %q", command.TaskID)
				return nil
			}
		}
		rig.fake.mu.Unlock()
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID,
			"tasks": []any{background.taskState("running", "")}})
		require.NoError(t, err)
		finished := beginNativeKimiReconnect(t, rig, json.RawMessage(list), kimiRecoveryNativeParentPage(t, rig, foreground.nativeInvocation(""), background.nativeInvocation("")), foreground.nativePage(t, rig, "interrupted", ""), background.nativePage(t, rig, "", ""))
		waitFor(t, finished, "the reconnect restores the stopped foreground transcript")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the missing foreground task outcome is recovered")
		awaitKimiTestOutput(t, rig.agent.sink)
		assert.Equal(t, bgtask.StatusStopped, nativeTaskOf(t, rig.sink, foreground.taskID).Status)
		assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, background.taskID).Status)
		row, child := childOf(t, rig.sink, "session_1/agent-0")
		assert.Equal(t, bgtask.StatusStopped, row.Status)
		assert.False(t, rig.agent.ActiveChildTurnState(row.RowKey).Active)
		assert.Contains(t, child.TurnActives()[foregroundBefore:], false)
		backgroundRow, _ := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusRunning, backgroundRow.Status)
		assert.True(t, rig.agent.ActiveChildTurnState(backgroundRow.RowKey).Active)
		assert.NotContains(t, backgroundSink.TurnActives()[backgroundBefore:], false)
		last, _ := rig.sink.LastTurnActive()
		assert.True(t, last)
	})

	t.Run("restores suspended and working swarm member turns", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.started","turn":`+kimiRecoveryNativeParent+`}`)
		var swarm map[string]any
		require.NoError(t, json.Unmarshal([]byte(`{"type":"tool_call","item_id":"call_swarm","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"AgentSwarm","stage":"executing","input":{"description":"Audit the packages","items":["A","B"],"prompt_template":"Check {{item}}."},"agent_refs":[{"agent_id":"agent-0","role":"member","swarm_index":0,"task_id":"member-0"},{"agent_id":"agent-1","role":"member","swarm_index":1,"task_id":"member-1"}]}`), &swarm))
		item, err := json.Marshal(swarm)
		require.NoError(t, err)
		native.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":`+string(item)+`}`)
		states := make([]map[string]any, 0, 2)
		for i, id := range []string{"agent-0", "agent-1"} {
			spec := kimiRecoveryChild{agentID: id, callID: "call_swarm", taskID: fmt.Sprintf("member-%d", i)}
			state := spec.taskState("running", "")
			state["swarm_index"], state["description"] = i, id
			states = append(states, state)
			live := maps.Clone(state)
			live["type"], live["session_id"], live["agent_id"] = contracts.KimiEventTask, rig.sessionID(), kimiMainAgentID
			raw, err := json.Marshal(live)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			run := rig.agent.run(id)
			waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "each swarm member receives its initial native page")
			rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), id, `{"type":"turn.started","turn":`+kimiRecoveryNativeParent+`}`))
		}
		const failed = `{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[]}`
		const retry = `{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[],"items":[]}`
		firstPage := json.RawMessage(failed[:len(failed)-1] + `,"items":[]}`)
		rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-1", `{"type":"turn.ended","turn":`+failed+`}`))
		states[0]["phase"], states[1]["phase"] = "suspended", "working"
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID, "tasks": states})
		require.NoError(t, err)
		_, suspended := childOf(t, rig.sink, "session_1/agent-0")
		_, working := childOf(t, rig.sink, "session_1/agent-1")
		beforeSuspended, beforeWorking := len(suspended.TurnActives()), len(working.TurnActives())
		frames := []any{json.RawMessage(list), kimiRecoveryNativeParentPage(t, rig, swarm),
			json.RawMessage(kimiConversationTestPage(t, rig.sessionID(), "agent-0", firstPage)),
			json.RawMessage(kimiConversationTestPage(t, rig.sessionID(), "agent-1", firstPage, json.RawMessage(retry)))}
		finished := beginNativeKimiReconnect(t, rig, frames...)
		waitFor(t, finished, "the reconnect restores suspended and retrying native child turns")
		suspendedEvents, workingEvents := suspended.TurnActives()[beforeSuspended:], working.TurnActives()[beforeWorking:]
		require.NotEmpty(t, suspendedEvents)
		assert.False(t, suspendedEvents[len(suspendedEvents)-1])
		require.NotEmpty(t, workingEvents)
		assert.True(t, workingEvents[len(workingEvents)-1])
		beforeRepeat := len(working.TurnActives())
		repeated := beginNativeKimiReconnect(t, rig, frames...)
		waitFor(t, repeated, "a fresh page confirms the already-running retry")
		assert.NotContains(t, working.TurnActives()[beforeRepeat:], false, "replayed historical failure cannot end the current retry")
		identity := kimiInvocationIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", toolCallID: "call_swarm"}
		for i, actor := range []string{"agent-0", "agent-1"} {
			marker, exists := rig.sink.BackgroundTask("session_1/" + actor)
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
			assert.Equal(t, bgtask.KindSubagent, marker.Kind)
			want := bgtask.StatusFailed
			if i == 1 {
				want = bgtask.StatusRunning
			}
			assert.Equal(t, want, marker.Status, "native headers determine current activity independently from the coordinator task")
			task, exists := rig.sink.BackgroundTask(fmt.Sprintf("session_1/task/main/member-%d", i))
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
			assert.Equal(t, bgtask.KindWorkflow, task.Kind)
			assert.Equal(t, bgtask.StatusRunning, task.Status)
			assert.Equal(t, kimiInvocationGroupKey(identity), task.GroupKey)
		}
	})

	t.Run("a background task that the gap started or ended", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task_1","kind":"shell","status":"running","detached":true,"command":"npm run build"}`))
		rig.fake.mu.Lock()
		rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
			outcome, commandText := "failed", "npm run build"
			if command.TaskID == "task_4" {
				outcome, commandText = "killed", "sleep 100"
			} else {
				assert.Equal(t, "task_1", command.TaskID, "historical output must not discover a task")
			}
			return map[string]any{"task_id": command.TaskID, "kind": "shell", "status": "ended", "outcome": outcome, "detached": true, "command": commandText}
		}
		rig.fake.mu.Unlock()
		rig.agent.Mu.Lock()
		oldEnd := rig.agent.attachedAt.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		rig.agent.Mu.Unlock()
		tool := func(id, taskID, command, startedAt string) map[string]any {
			return map[string]any{"type": "tool_call", "item_id": id, "name": "Bash", "status": "ended", "outcome": "completed",
				"created_at": startedAt, "started_at": startedAt, "task_id": taskID,
				"input": map[string]any{"command": command, "run_in_background": true}, "output": "Background work continues."}
		}
		const currentStart = "2026-11-18T00:00:00Z"
		oldTurn := map[string]any{"turn_id": "old", "ordinal": 0, "kind": "agent", "status": "ended", "outcome": "completed",
			"started_at": oldEnd, "ended_at": oldEnd, "steps": []any{}, "items": []any{tool("historical", "task_3", "make old", oldEnd)}}
		current := map[string]any{"turn_id": "current", "ordinal": 1, "kind": "agent", "status": "running",
			"started_at": currentStart, "steps": []any{}, "items": []any{tool("missed", "task_4", "sleep 100", currentStart)}}
		oldRaw, err := json.Marshal(oldTurn)
		require.NoError(t, err)
		currentRaw, err := json.Marshal(current)
		require.NoError(t, err)
		list := json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"task_2","kind":"shell","status":"running","detached":true,"command":"npm run watch","description":"Watch"},{"task_id":"task_5","kind":"other","status":"running","detached":false}]}`)
		page := json.RawMessage(kimiConversationTestPage(t, rig.sessionID(), "main", oldRaw, currentRaw))
		finished := beginNativeKimiReconnect(t, rig, list, page)
		waitFor(t, finished, "the reconnect applies the native running list and public references")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the exact native task outcomes finish recovery")
		awaitKimiTestOutput(t, rig.agent.sink)
		row, exists := rig.sink.BackgroundTask("session_1/task/main/task_4")
		require.True(t, exists, "a task discovered from its public reference opens and closes")
		assert.Equal(t, bgtask.StatusStopped, row.Status)
		row, exists = rig.sink.BackgroundTask("session_1/task/main/task_1")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
		row, exists = rig.sink.BackgroundTask("session_1/task/main/task_2")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.Equal(t, "npm run watch", row.Title)
		assert.True(t, row.TitleIsCommand)
		assert.Equal(t, "Watch", row.Description)
		_, exists = rig.sink.BackgroundTask("session_1/task/main/task_3")
		assert.False(t, exists, "finished work before attachment is history")
		_, exists = rig.sink.BackgroundTask("session_1/task/main/task_5")
		assert.False(t, exists, "a native non-shell/non-child task has no registry row")
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
	})

	t.Run("recovers a running background child and a missed background completion", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		older := kimiRecoveryChild{agentID: "agent-1", callID: "call_background", taskID: "older-background", background: true, omitTaskReference: true}
		older.startNative(t, rig)
		newer := kimiRecoveryChild{agentID: "agent-9", callID: "call_gone", taskID: "newer-background", background: true}
		newer.startNative(t, rig)
		rig.fake.mu.Lock()
		rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
			assert.Equal(t, older.taskID, command.TaskID)
			return older.taskState("ended", "completed")
		}
		rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
			return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": "The earlier background audit completed."}}
		}
		rig.fake.mu.Unlock()
		unjoined := kimiRecoveryChild{agentID: "agent-10", callID: "unjoined-call", taskID: "unjoined-background", background: true}
		unjoinedState := unjoined.taskState("running", "")
		unjoinedState["description"] = "Unjoined background audit"
		state := newer.taskState("running", "")
		state["description"] = "Background audit"
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID, "tasks": []any{state, unjoinedState}})
		require.NoError(t, err)
		_, newerSink := childOf(t, rig.sink, "session_1/agent-9")
		beforeNewer := len(newerSink.TurnActives())
		finished := beginNativeKimiReconnect(t, rig, json.RawMessage(list), kimiRecoveryNativeParentPage(t, rig, older.nativeInvocation(""), newer.nativeInvocation("")),
			older.nativePage(t, rig, "completed", ""), newer.nativePage(t, rig, "", ""), unjoined.nativePage(t, rig, "", ""))
		waitFor(t, finished, "the reconnect progresses past the original subscriptions")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the missed background completion and discovered subscription finish")
		waitFor(t, func() bool {
			return rig.agent.ActiveChildTurnState("session_1/agent-10").Active && rig.agent.run("agent-10").conversationRecovery.Load() == nil
		}, "the unjoined child projects its native page")
		awaitKimiTestOutput(t, rig.agent.sink)
		row, child := childOf(t, rig.sink, "session_1/agent-9")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		task := nativeTaskOf(t, rig.sink, "newer-background")
		assert.Equal(t, "Background audit", task.Title)
		assert.Contains(t, child.TurnActives()[beforeNewer:], true, "the running activity survives replay")
		assert.NotContains(t, child.TurnActives()[beforeNewer:], false)
		unjoinedRow, _ := childOf(t, rig.sink, "session_1/agent-10")
		assert.Equal(t, bgtask.StatusRunning, unjoinedRow.Status)
		assert.Equal(t, "Unjoined background audit", unjoinedRow.Title)
		// Native task state carries no child turn identity. Two new records alone
		// cannot authorize stopping that particular running turn.
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-10", agent.StopContext{}), agent.ErrChildOperationUnsupported)
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-9", agent.StopContext{}), agent.ErrChildOperationUnsupported)
		rig.fake.mu.Lock()
		commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
		rig.fake.mu.Unlock()
		var stops []fakeKimiTaskCommand
		for _, command := range commands {
			if command.Type == kimiFrameTaskStop {
				stops = append(stops, command)
			}
		}
		assert.Empty(t, stops, "a native task address alone cannot authorize child-turn interruption")
		ended, endedSink := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, ended.Status)
		oldTask, exists := rig.sink.BackgroundTask("session_1/task/main/older-background")
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusSucceeded, oldTask.Status)
		assert.False(t, rig.agent.ActiveChildTurnState(ended.RowKey).Active)
		waitFor(t, func() bool {
			return len(endedSink.LeapMuxNotifications()) == 1
		}, "the historical report reaches the child without inventing a parent invocation")
		assert.Contains(t, fmt.Sprint(endedSink.LeapMuxNotifications()), "The earlier background audit completed.")
		require.Len(t, rig.sink.PersistedNotifications(), 1)
		assert.True(t, rig.sink.PersistedNotifications()[0].TranscriptOnly, "an unbound task report is history, not a current-turn publication")
	})

}

func TestKimiResyncWaitsForTheSubagentRegistryUpdate(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	ctx := testutil.DeadlineContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseRegistration := sync.OnceFunc(func() { close(release) })
	defer releaseRegistration()
	pauseRegistration := sync.OnceFunc(func() {
		close(entered)
		<-release
	})
	rig.sink.OnUpsertBackgroundTask = func(update bgtask.Upsert) {
		if update.RowKey == "session_1/task/main/swarm-member" && update.Title == "Audit #1" {
			pauseRegistration()
		}
	}
	var swarm map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type":"tool_call","item_id":"call_swarm","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"AgentSwarm","stage":"executing","input":{"description":"Audit the packages","items":["A"],"prompt_template":"Check {{item}}."},"agent_refs":[{"agent_id":"agent-9","role":"member","swarm_index":0,"task_id":"swarm-member"}]}`), &swarm))
	childPage := json.RawMessage(kimiConversationTestPage(t, rig.sessionID(), "agent-9", json.RawMessage(`{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}`)))
	finished := beginNativeKimiReconnect(t, rig,
		json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-9","parent_tool_call_id":"call_swarm","swarm_index":0,"description":"Audit #1"}]}`),
		kimiRecoveryNativeParentPage(t, rig, swarm), childPage)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native replay did not reach its registry update")
	}
	assert.False(t, finished(), "receiving frames does not complete a paused registry update")
	releaseRegistration()
	waitFor(t, finished, "the native replay completes its registry update")
	waitFor(t, func() bool {
		return rig.agent.ActiveChildTurnState("session_1/agent-9").Active && rig.agent.run("agent-9").conversationRecovery.Load() == nil
	}, "the adopted child publishes its native running turn")
	awaitKimiTestOutput(t, rig.agent.sink)
	row, _ := childOf(t, rig.sink, "session_1/agent-9")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	identity := kimiInvocationIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", toolCallID: "call_swarm"}
	outcome := nativeTaskOf(t, rig.sink, "swarm-member")
	assert.Equal(t, bgtask.KindWorkflow, outcome.Kind)
	assert.Equal(t, kimiInvocationGroupKey(identity), outcome.GroupKey)
	assert.Equal(t, "Audit the packages", outcome.GroupLabel)
	assert.Equal(t, "Audit the packages", row.Title)
	assert.Equal(t, "Audit #1", outcome.Title)
	assert.Equal(t, row.ChildAgentID, outcome.ChildAgentID)
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

// A complete native page determines activity. Flat settings do not prove a turn ended.
func TestKimiResyncUsesNativeTranscriptActivity(t *testing.T) {
	t.Parallel()
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("running=%t", running), func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			const turn = `{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}`
			native.conversationFrame(t, `{"type":"turn.started","turn":`+turn+`}`)
			turns := `[]`
			if running {
				turns = `[` + turn + `]`
			}
			page := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","turns":`+turns+`,"has_more":false}`))
			finished := beginNativeKimiReconnect(t, rig, page)
			waitFor(t, finished, "the native reconnect dispatches its complete page")
			last, _ := rig.sink.LastTurnActive()
			assert.Equal(t, running, last)
			assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
		})
	}
	t.Run("keeps the last activity until the initial page arrives", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		ctx := testutil.DeadlineContext(t)
		pageHeld, releasePage := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(releasePage) })
		defer release()
		var hold sync.Once
		rig.fake.mu.Lock()
		rig.fake.beforeConversation = func(fakeKapSubscribe, string) {
			hold.Do(func() {
				close(pageHeld)
				select {
				case <-releasePage:
				case <-ctx.Done():
				}
			})
		}
		rig.fake.mu.Unlock()
		finished := beginNativeKimiReconnect(t, rig)
		select {
		case <-pageHeld:
		case <-ctx.Done():
			t.Fatal("the reconnect did not reach its held page")
		}
		assert.False(t, finished())
		last, _ := rig.sink.LastTurnActive()
		assert.True(t, last, "the subscribe response and flat idle state cannot end the last known turn")
		release()
		waitFor(t, finished, "the complete empty page reaches dispatch")
		last, _ = rig.sink.LastTurnActive()
		assert.False(t, last, "only the complete page clears the previous turn")
	})
}

func TestKimiResyncIgnoresASessionItDoesNotDrive(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	before := len(rig.fake.routes())
	rig.agent.resyncSession(context.Background(), "session_other")
	rig.agent.resyncSession(context.Background(), "../x")
	assert.Len(t, rig.fake.routes(), before, "a session of the past, or an id the server would not issue, sends nothing")
}

// A context clear ends the conversation, and the subagents of the old session
// go with it: what one streamed is closed in its own transcript, and its route
// is gone. Its registry row stays open on purpose (see ClearContext).
func TestKimiRuntimeExitSettlesNativeChildTools(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task"}
	spec.startNative(t, rig)
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.conversationAgentFrame(t, "agent-0", `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call_grep","step_index":1,"name":"Grep","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"pattern":"x"}}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	var grepSpan string
	for _, message := range child.Messages() {
		view, err := decodeKimiConversationRecord(message.Content)
		require.NoError(t, err)
		if view.item != nil && view.item.ItemID == "call_grep" && view.record.Role == contracts.KimiConversationRecordRoleRequest {
			grepSpan = message.SpanID
		}
	}
	require.NotEmpty(t, grepSpan)

	rig.agent.SetStoppedForTest(true)
	rig.agent.SimulateExitForTest()
	require.NoError(t, rig.agent.Wait())
	messages := child.Messages()
	require.NotEmpty(t, messages)
	var closed bool
	for _, message := range messages {
		if message.SpanID == grepSpan && message.Closing {
			closed = true
			assert.Equal(t, agent.MessageCompletionInterrupted, message.Completion)
		}
	}
	assert.True(t, closed, "the child's open native tool closes in its original transcript")
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	require.Error(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "A message after exit."}))
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")))
	task, exists := rig.sink.BackgroundTask("session_1/task/main/child-task")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, task.Status, "the Worker finalizes unresolved task outcomes after provider Wait returns")
}

func TestKimiResumeSessionReportsAProfileWriteThatFails(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.store("session_stored", fakeKapSession{Model: "kimi-text", Permission: "manual"})
	fake.reply("POST "+kimiSessionPath("session_stored", "/profile"), fakeKapReply{Code: 50000, Msg: "profile locked"})
	a, _, opts := newConnectedKimiAgent(t, server.URL, agent.Options{
		ResumeSessionID: "session_stored", Options: options(agent.OptionIDPermissionMode, contracts.KimiModeAuto),
	})
	err := a.openStartupSession(opts, 30*time.Second)
	require.ErrorContains(t, err, "configure the resumed Kimi Code session")
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	assert.Empty(t, sessionID, "a failed resume leaves no session behind")
}

func TestKimiResyncStopsWhenSessionSubscriptionFails(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.agent.stream.forget("session_1")
	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeSessionNotFound })
	rig.agent.resyncSession(t.Context(), "session_1")
	assert.Len(t, rig.fake.subscribeFrames(), 2, "the native subscription reports the unavailable session")
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

// Missing task evidence leaves a known child running, even after a native reconnect.
func TestKimiResyncWithoutTheTaskListClosesNoUnlistedSubagent(t *testing.T) {
	t.Parallel()
	for _, refusedWatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused_watch=%t", refusedWatch), func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			spec := kimiRecoveryChild{agentID: "agent-0", callID: "spawn", taskID: "task-child", omitTaskReference: true}
			spec.startNative(t, rig)
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			before := len(child.TurnActives())
			frames := []any{kimiRecoveryNativeParentPage(t, rig, spec.nativeInvocation("")), spec.nativePage(t, rig, "", "")}
			if refusedWatch {
				rig.fake.mu.Lock()
				rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) {
					if command.Type == kimiFrameTaskWatch {
						return 40401, "task state unavailable"
					}
					return 0, "success"
				}
				rig.fake.mu.Unlock()
				frames = append(frames, json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
			}
			finished := beginNativeKimiReconnect(t, rig, frames...)
			waitFor(t, finished, "the native reconnect applies its replay")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the attempted task recovery finishes")
			awaitKimiTestOutput(t, rig.agent.sink)
			row, _ := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.True(t, rig.agent.ActiveChildTurnState(row.RowKey).Active)
			assert.NotContains(t, child.TurnActives()[before:], false)
			expectedTaskStatus := bgtask.StatusRunning
			if refusedWatch {
				expectedTaskStatus = bgtask.StatusEndedWithUnknownOutcome
			}
			assert.Equal(t, expectedTaskStatus, nativeTaskOf(t, rig.sink, spec.taskID).Status)
			last, _ := rig.sink.LastTurnActive()
			assert.True(t, last)
			rig.fake.mu.Lock()
			commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
			rig.fake.mu.Unlock()
			if refusedWatch {
				require.Len(t, commands, 2)
				assert.Equal(t, kimiFrameTaskWatch, commands[0].Type)
				assert.Equal(t, kimiFrameTaskUnwatch, commands[1].Type)
			} else {
				assert.Empty(t, commands)
			}
			assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/tasks")))
			assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
		})
	}
}

func TestKimiRuntimeReplacementRejectsLateOldEvents(t *testing.T) {
	t.Parallel()
	old := newKimiServiceTestRig(t)
	old.agent.closeStream()
	replacementSink, err := old.output.NewRootSink("test-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE)
	require.NoError(t, err)
	fake, server := newFakeKap(t)
	fake.mu.Lock()
	fake.nextSession = 1
	fake.mu.Unlock()
	replacement, opts := newUnconnectedKimiAgent(t, agent.Options{}, replacementSink)
	require.NoError(t, replacement.connect(replacement.Context(), server.URL, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(replacement.closeStream)
	require.NoError(t, replacement.openStartupSession(opts, 30*time.Second))
	// The ACK precedes initial state. Capture only after that state has finished
	// changing the replacement's scope, so only late old events remain under test.
	main := replacement.run(kimiMainAgentID)
	waitFor(t, func() bool {
		return main.conversationRecovery.Load() == nil && !agent.OutputQueueFor(replacement.sink).HasPending()
	}, "the replacement applies its initial conversation page and output")
	replacement.HandleOutput([]byte(`{"type":"todo","session_id":"session_2","agent_id":"main","items":[{"title":"Replacement list","status":"pending"}]}`))
	captured := agent.CaptureTranscript(replacementSink, agent.MessageContent{}, agent.SpanInfo{})
	var frames [][]byte
	for ordinal := range 50 {
		turnID := fmt.Sprintf("stale-%d", ordinal)
		header := fmt.Sprintf(`"turn_id":%q,"ordinal":%d,"kind":"agent","started_at":"2026-11-18T00:00:00Z","steps":[]`, turnID, ordinal)
		for _, frame := range []string{
			`{"type":"turn.started","turn":{` + header + `,"status":"running"}}`,
			fmt.Sprintf(`{"type":"item.started","turn_id":%q,"item":{"type":"tool_call","item_id":"call","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{}}}`, turnID),
			fmt.Sprintf(`{"type":"item.ended","turn_id":%q,"item":{"type":"assistant_message","item_id":"answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Stale answer"}}`, turnID),
			`{"type":"turn.ended","turn":{` + header + `,"status":"ended","outcome":"completed"}}`,
		} {
			frames = append(frames, kimiConversationTestFrame(t, "session_1", kimiMainAgentID, frame))
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for _, frame := range frames {
			old.agent.HandleOutput(frame)
		}
	})
	old.agent.HandleOutput([]byte(`{"type":"todo","session_id":"session_1","agent_id":"main","items":[{"title":"Stale list","status":"done"}]}`))
	wg.Wait()
	assert.True(t, captured.FrozenContent().Publication.Owner().IsCurrent(), "old native frames cannot replace the new activity scope")
	row, err := old.queries.GetAgentByID(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Equal(t, "session_2", row.AgentSessionID)
	items, err := old.output.LoadTodos(t.Context(), "test-agent")
	require.NoError(t, err)
	assert.Equal(t, []todoevents.Item{{Content: "Replacement list", Status: todoevents.StatusPending}}, items)
	assert.Equal(t, agent.TurnState{}, replacement.PublishTurnActive())
}

func TestKimiNewSessionRetainsStateBeforeSubscriptionAcknowledgement(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{})
	fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
		for _, state := range fake.sessionStateFrames(sub.SessionID) {
			data, err := json.Marshal(state)
			assert.NoError(t, err)
			var frame kimiFrame
			assert.NoError(t, json.Unmarshal(data, &frame))
			a.stream.observeSessionState(frame)
			a.HandleOutput(data)
		}
		raw := fmt.Sprintf(`{"type":"goal","session_id":%q,"agent_id":"main","goal":{"objective":"Initial goal","status":"paused","turns_used":0,"tokens_used":0,"wall_clock_ms":0}}`, sub.SessionID)
		a.HandleOutput([]byte(raw))
		return kimiCodeOK
	})
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	goal, exists := sink.LastGoal()
	require.True(t, exists, "the initial state must survive until the new session is adopted")
	assert.Equal(t, "Initial goal", goal.Objective)
	assert.True(t, goal.Snapshot)
}

func TestKimiFreshStartupRefusalDiscardsUnacceptedState(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{})
	fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
		raw := fmt.Sprintf(`{"type":"goal","session_id":%q,"agent_id":"main","goal":{"objective":"Unaccepted goal","status":"active","turns_used":0,"tokens_used":0,"wall_clock_ms":0}}`, sub.SessionID)
		a.HandleOutput([]byte(raw))
		return 40001
	})
	err := a.openStartupSession(opts, 30*time.Second)
	require.Error(t, err)
	require.Len(t, fake.requestsTo("POST "+kimiRouteSessions), 1)
	assert.NotErrorIs(t, err, agent.ErrContextClearUnsupported)
	assert.Empty(t, sink.Goals())
	assert.Empty(t, sink.GoalClearSnapshots())
	a.Mu.Lock()
	assert.Empty(t, a.sessionID)
	a.Mu.Unlock()
	assert.Empty(t, sink.MessageSessionID())
	a.dispatchMu.Lock()
	assert.Nil(t, a.pendingSession)
	a.dispatchMu.Unlock()
}

func TestKimiSessionAdmissionLimitsEmptyFramesAndDiscardsOverflow(t *testing.T) {
	t.Parallel()
	pending := &kimiSessionAdmission{sessionID: "session_new", bytes: kimiStreamReadLimit - 256}
	pending.append(kimiFrame{SessionID: "session_new"})
	require.NoError(t, pending.err)
	require.Len(t, pending.frames, 1)
	pending.append(kimiFrame{SessionID: "session_new"})
	require.ErrorContains(t, pending.err, "memory limit")
	assert.Empty(t, pending.frames)
	pending.append(kimiFrame{SessionID: "session_new"})
	assert.Empty(t, pending.frames)
}

func TestKimiReconnectUsesNativeStateWithoutRemovedRESTRoutes(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.agent.resyncSession(t.Context(), rig.sessionID())
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/tasks")))
}
