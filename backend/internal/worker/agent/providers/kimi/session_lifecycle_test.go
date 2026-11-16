package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
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
	assert.Equal(t, []string{"session_1"}, subscribes[0].IDs)
}

func TestKimiRuntimeExitEndsRootAndChildCompaction(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	childID := adoptNativeKimiChild(t, rig.agent, "agent-1")
	for _, id := range []string{kimiMainAgentID, "agent-1"} {
		rig.publishMarker(t, id, "compaction", map[string]any{"agentId": id, "trigger": "auto", "phase": "started"})
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
		fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) { return 0, kimiAck{NotFound: sub.IDs} })
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
				fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) { return 0, kimiAck{NotFound: sub.IDs} })
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
	rig.fake.closeConnections()

	waitFor(t, func() bool {
		return rig.sink.PermissionMode() == contracts.KimiModePlan &&
			rig.sink.SettingsRefreshCount() > 0 && rig.sink.LastSettingsRefresh().Options[kimiOptionSwarmMode] == kimiSwarmOn
	}, "the reconnect reports the plan mode and the swarm mode the stream missed")
	current := agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, contracts.KimiModePlan, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOn, current[kimiOptionSwarmMode])
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

// beginNativeKimiReconnect supplies native replay frames and returns their dispatch completion check.
func beginNativeKimiReconnect(t *testing.T, rig *kimiTestRig, frames ...any) func() bool {
	t.Helper()
	rig.agent.dispatchMu.Lock()
	sequence := rig.agent.run(kimiMainAgentID).transcript.seq + 1000
	rig.agent.dispatchMu.Unlock()
	sessionID := rig.sessionID()
	marker := map[string]any{"type": contracts.KimiEventTranscriptOps, "session_id": sessionID,
		"payload": map[string]any{"type": contracts.KimiEventTranscriptOps, "agent_id": kimiMainAgentID, "seq": sequence, "ops": []any{}}}
	replay := append(append([]any(nil), frames...), marker)
	before := len(rig.fake.subscribeFrames())
	rig.fake.update(sessionID, func(session *fakeKapSession) { session.ReplayFrames = replay })
	rig.fake.setAckFor(nil)
	rig.fake.closeConnections()
	return func() bool {
		if len(rig.fake.subscribeFrames()) <= before || agent.OutputQueueFor(rig.agent.sink).HasPending() || !rig.agent.dispatchMu.TryLock() {
			return false
		}
		defer rig.agent.dispatchMu.Unlock()
		return rig.agent.run(kimiMainAgentID).transcript.seq >= sequence
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
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.ReplayFrames = []any{
				json.RawMessage(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":1,"snapshot":{"items":[],"interactions":[{"interactionId":"approval_9","interactionKind":"approval","state":"pending","toolCallId":"call_9"}]}}}`), approval, question,
			}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool { return rig.sink.PublishedControlCount() == 2 }, "both native interactions reach the user")
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
		require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_9", "allow", nil, nil), agent.StopContext{}))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "interactions", "approval_9", "")), 1)
	})
	t.Run("withdraws a request the server resolved during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.feed(t, approvalEvent("approval_2", map[string]any{"kind": "command", "command": "pwd"}))
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.ReplayFrames = []any{json.RawMessage(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":1,"snapshot":{"items":[],"interactions":[{"interactionId":"approval_2","interactionKind":"approval","state":"pending"}]}}}`)}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool { return len(rig.sink.CanceledControls()) == 1 }, "the complete native reset withdraws the resolved banner")
		assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
		assert.Equal(t, 2, rig.sink.PublishedControlCount(), "an already published pending request is not published twice")
		require.ErrorIs(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil), agent.StopContext{}), errKimiControlGone)
	})
	t.Run("clears an ended main turn while background work keeps the session busy", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
		rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{}}}`)
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.Busy = true
			session.ReplayFrames = []any{
				json.RawMessage(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"background","kind":"shell","status":"running","detached":true,"description":"Background work"}`),
				json.RawMessage(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":3,"snapshot":{"items":[{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","ordinal":1,"state":"interrupted","frames":[{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{}}]}]}],"interactions":[]}}}`),
			}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool { last, _ := rig.sink.LastTurnActive(); return !last }, "the native cancelled turn releases the main input queue")
		var closing *agenttest.Message
		for _, message := range rig.sink.Messages() {
			if message.Closing {
				copy := message
				closing = &copy
			}
		}
		require.NotNil(t, closing)
		assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
		messages := rig.sink.Messages()
		require.NotEmpty(t, messages)
		assert.True(t, messages[len(messages)-1].TurnEnd)
		rig.fake.setBusy("session_1", false)
		require.NoError(t, rig.agent.SendInput("Next.", nil))
	})
	t.Run("starts a main turn that started during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.Busy = true
			session.ReplayFrames = []any{json.RawMessage(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":1,"snapshot":{"items":[{"kind":"turn","turnId":"t3","state":"running","origin":{"kind":"other","payload":{"kind":"system_trigger"}},"steps":[]}],"interactions":[]}}}`)}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool { last, _ := rig.sink.LastTurnActive(); return last }, "the recovered native running turn holds the queue")
		assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(), "an autonomous native origin takes no steer")
		var busy *agent.AgentBusyError
		require.ErrorAs(t, rig.agent.SendInput("Next.", nil), &busy)
		rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t3","state":"completed","origin":{"kind":"other","payload":{"kind":"system_trigger"}}}}`)
		last, _ := rig.sink.LastTurnActive()
		assert.False(t, last)
	})
	t.Run("keeps a main user turn that still runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.Busy = true
			session.ReplayFrames = []any{json.RawMessage(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":2,"snapshot":{"items":[{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[]}],"interactions":[]}}}`)}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool {
			rig.agent.dispatchMu.Lock()
			defer rig.agent.dispatchMu.Unlock()
			return rig.agent.run("main").transcript.seq == 2
		}, "the native reset applies its authoritative running header")
		assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
	})
	t.Run("loads and re-subscribes a session the server dropped", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		var dropped sync.Once
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			first := false
			dropped.Do(func() { first = true })
			if first {
				return 0, kimiAck{NotFound: sub.IDs}
			}
			for _, state := range rig.fake.sessionStateFrames(sub.SessionID) {
				rig.fake.push(t, state)
			}
			approval := approvalEvent("approval_9", map[string]any{"kind": "command", "command": "ls"})
			approval["session_id"] = sub.SessionID
			rig.fake.push(t, approval)
			return 0, kimiAck{Accepted: sub.IDs}
		})
		rig.fake.closeConnections()
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
}

func (child kimiRecoveryChild) taskState(status, outcome string) map[string]any {
	state := map[string]any{"task_id": child.taskID, "kind": "subagent", "status": status,
		"detached": child.background, "child_agent_id": child.agentID, "parent_tool_call_id": child.callID, "description": "Reviewer"}
	if outcome != "" {
		state["outcome"] = outcome
	}
	return state
}

func (child kimiRecoveryChild) start(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	rig.agent.dispatchMu.Lock()
	sequence := rig.agent.run(kimiMainAgentID).transcript.seq + 1
	rig.agent.dispatchMu.Unlock()
	native.transcriptOps(t, sequence,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":%q,"toolCallId":%q,"name":"Agent","state":"running","input":{"description":"Reviewer","prompt":"Review the diff.","run_in_background":%t},"agentRefs":[{"agentId":%q,"role":"child"}]}}`, child.callID, child.callID, child.background, child.agentID))
	task := child.taskState("running", "")
	task["type"], task["session_id"], task["agent_id"] = contracts.KimiEventTask, rig.sessionID(), kimiMainAgentID
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	rig.agent.HandleOutput(raw)
	native.transcriptAgentOps(t, child.agentID, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Review the diff."}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Found it."}}`)
}

func (child kimiRecoveryChild) reset(t *testing.T, rig *kimiTestRig, turnState, stepState, failure string) json.RawMessage {
	t.Helper()
	rig.agent.Mu.Lock()
	endedAt := rig.agent.attachedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	rig.agent.Mu.Unlock()
	turn := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": turnState,
		"origin": map[string]any{"kind": "other"}, "prompt": "Review the diff.",
		"endedAt": endedAt,
		"steps": []any{map[string]any{"kind": "step", "stepId": "t0.1", "turnId": "t0", "ordinal": 1, "state": stepState,
			"frames": []any{map[string]any{"kind": "text", "frameId": "answer", "role": "assistant", "text": "Found it."}}}}}
	if failure != "" {
		turn["error"] = failure
	}
	return nativeKimiRecoveryReset(t, rig.sessionID(), child.agentID, 20, turn)
}

func nativeKimiRecoveryReset(t *testing.T, sessionID, agentID string, sequence int64, items ...map[string]any) json.RawMessage {
	t.Helper()
	if items == nil {
		items = []map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"type": contracts.KimiEventTranscriptReset, "session_id": sessionID,
		"payload": map[string]any{"type": contracts.KimiEventTranscriptReset, "agent_id": agentID, "seq": sequence, "has_more_older": false,
			"snapshot": map[string]any{"items": items, "interactions": []any{}, "attachments": []any{}, "prompts": []any{}}}})
	require.NoError(t, err)
	return raw
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
		turn, step, outcome, report, failure string
		status                               bgtask.Status
		completion                           agent.MessageCompletion
	}{
		"completed": {turn: "completed", step: "completed", outcome: "completed", report: "All good.", status: bgtask.StatusSucceeded, completion: agent.MessageCompletionComplete},
		"failed":    {turn: "failed", step: "failed", outcome: "failed", failure: "provider exploded", status: bgtask.StatusFailed, completion: agent.MessageCompletionError},
		"cancelled": {turn: "cancelled", step: "interrupted", outcome: "killed", status: bgtask.StatusStopped, completion: agent.MessageCompletionInterrupted},
	} {
		t.Run("a subagent that "+name+" during the gap closes with the status the server reports", func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			spec := kimiRecoveryChild{agentID: "agent-0", callID: "call_child", taskID: "task-child"}
			spec.start(t, rig)
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
				spec.reset(t, rig, tc.turn, tc.step, tc.failure),
			}
			finished := beginNativeKimiReconnect(t, rig, frames...)
			waitFor(t, finished, "the native reconnect restores the child transcript")
			waitFor(t, rowStatus(rig, "session_1/agent-0"), "task.watch restores the missed outcome")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the task outcome finishes recovery")
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
			assert.Equal(t, []bool{true, false}, child.TurnActives())
			var foundText, foundFailure bool
			for _, message := range child.Messages() {
				var assembled map[string]string
				if json.Unmarshal(message.Content, &assembled) != nil || assembled[contracts.AssembledMessageFieldType] != contracts.AssembledMessageType {
					continue
				}
				if assembled[contracts.AssembledMessageFieldText] == "Found it." {
					foundText = true
					assert.Equal(t, string(tc.completion), assembled[contracts.AssembledMessageFieldCompletion])
				}
				if tc.failure != "" && assembled[contracts.AssembledMessageFieldText] == tc.failure {
					foundFailure = true
					assert.Equal(t, string(agent.MessageCompletionError), assembled[contracts.AssembledMessageFieldCompletion])
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
		native.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`)
		result := "agent_id: agent-2\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nDone.\n\nresume_hint: Continue with Agent(resume=\"agent-2\", prompt=\"...\")."
		parent := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": "running", "origin": map[string]any{"kind": "user"},
			"steps": []any{map[string]any{"kind": "step", "stepId": "t0.1", "ordinal": 1, "state": "running", "frames": []any{
				map[string]any{"kind": "tool", "frameId": "spawn", "toolCallId": "call_gone", "name": "Agent", "state": "done", "input": map[string]any{"description": "Reviewer", "prompt": "Review it."}, "output": result,
					"agentRefs": []any{map[string]any{"agentId": "agent-2", "role": "child"}}},
			}}}}
		child := kimiRecoveryChild{agentID: "agent-2", callID: "call_gone"}
		finished := beginNativeKimiReconnect(t, rig, nativeKimiRecoveryReset(t, rig.sessionID(), kimiMainAgentID, 20, parent), child.reset(t, rig, "completed", "", ""))
		waitFor(t, finished, "the reconnect applies the parent result and completed child transcript")
		row, _ := childOf(t, rig.sink, "session_1/agent-2")
		assert.Equal(t, bgtask.RoleChildActivity, row.Role)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
		assert.Equal(t, bgtask.KindSubagent, row.Kind)
		identity := kimiTranscriptToolIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "spawn", toolCallID: "call_gone"}
		outcome := invocationOutcomeOf(t, rig.sink, identity, "agent-2")
		assert.Equal(t, bgtask.StatusSucceeded, outcome.Status)
		assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
	})

	t.Run("recovers a foreground stop without ending a running background child", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		foreground := kimiRecoveryChild{agentID: "agent-0", callID: "call_child", taskID: "foreground-task"}
		background := kimiRecoveryChild{agentID: "agent-1", callID: "call_background", taskID: "background-task", background: true}
		foreground.start(t, rig)
		background.start(t, rig)
		rig.fake.mu.Lock()
		rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
			assert.Equal(t, foreground.taskID, command.TaskID)
			return foreground.taskState("ended", "killed")
		}
		rig.fake.mu.Unlock()
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID,
			"tasks": []any{background.taskState("running", "")}})
		require.NoError(t, err)
		finished := beginNativeKimiReconnect(t, rig, json.RawMessage(list), foreground.reset(t, rig, "cancelled", "interrupted", ""))
		waitFor(t, finished, "the reconnect restores the stopped foreground transcript")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the missing foreground task outcome is recovered")
		row, child := childOf(t, rig.sink, "session_1/agent-0")
		assert.Equal(t, bgtask.StatusStopped, row.Status)
		assert.Equal(t, []bool{true, false}, child.TurnActives())
		backgroundRow, backgroundSink := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusRunning, backgroundRow.Status)
		assert.Equal(t, []bool{true}, backgroundSink.TurnActives())
		last, _ := rig.sink.LastTurnActive()
		assert.True(t, last)
	})

	t.Run("restores suspended and working swarm member turns", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.transcriptOps(t, 1,
			`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
			`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"swarm","toolCallId":"call_swarm","name":"AgentSwarm","state":"running","input":{"description":"Audit the packages","items":["A","B"],"prompt_template":"Check {{item}}."},"agentRefs":[{"agentId":"agent-0","role":"member"},{"agentId":"agent-1","role":"member"}]}}`)
		states := make([]map[string]any, 0, 2)
		for i, id := range []string{"agent-0", "agent-1"} {
			spec := kimiRecoveryChild{agentID: id, callID: "call_swarm", taskID: fmt.Sprintf("member-%d", i)}
			state := spec.taskState("running", "")
			state["swarm_index"], state["description"] = i+1, id
			states = append(states, state)
			// Seed each task through a live task frame; partial lists would imply a missing sibling.
			live := maps.Clone(state)
			live["type"], live["session_id"], live["agent_id"] = contracts.KimiEventTask, rig.sessionID(), kimiMainAgentID
			raw, err := json.Marshal(live)
			require.NoError(t, err)
			rig.agent.HandleOutput(raw)
			native.transcriptAgentOps(t, id, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Check it."}}`)
		}
		native.transcriptAgentOps(t, "agent-1", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"other"}}}`)
		states[0]["phase"] = "suspended"
		states[1]["phase"] = "working"
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID, "tasks": states})
		require.NoError(t, err)
		rig.agent.Mu.Lock()
		endedAt := rig.agent.attachedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
		rig.agent.Mu.Unlock()
		failed := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": "failed", "origin": map[string]any{"kind": "other"}, "prompt": "Check it.", "endedAt": endedAt, "steps": []any{}}
		retry := map[string]any{"kind": "turn", "turnId": "t1", "ordinal": 1, "state": "running", "origin": map[string]any{"kind": "other", "payload": map[string]any{"kind": "retry"}}, "steps": []any{}}
		finished := beginNativeKimiReconnect(t, rig, json.RawMessage(list), nativeKimiRecoveryReset(t, rig.sessionID(), "agent-0", 20, failed), nativeKimiRecoveryReset(t, rig.sessionID(), "agent-1", 20, failed, retry))
		waitFor(t, finished, "the reconnect restores suspended and retrying native child turns")
		_, suspended := childOf(t, rig.sink, "session_1/agent-0")
		_, working := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, []bool{true, false}, suspended.TurnActives())
		assert.Equal(t, []bool{true, false, true}, working.TurnActives())
		repeated := beginNativeKimiReconnect(t, rig, nativeKimiRecoveryReset(t, rig.sessionID(), "agent-1", 21, failed, retry))
		waitFor(t, repeated, "a fresh reset confirms the already-running retry")
		assert.Equal(t, []bool{true, false, true, true}, working.TurnActives(), "each complete reset publishes its final state once")
		identity := kimiTranscriptToolIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "swarm", toolCallID: "call_swarm"}
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
			outcome := invocationOutcomeOf(t, rig.sink, identity, actor)
			assert.Equal(t, kimiInvocationGroupKey(identity), outcome.GroupKey)
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
		tool := func(id, taskID, command string) map[string]any {
			return map[string]any{"kind": "tool", "frameId": id, "toolCallId": id, "name": "Bash", "state": "done", "input": map[string]any{"command": command, "run_in_background": true},
				"output": "task_id: " + taskID + "\npid: 123\ndescription: " + command + "\nstatus: running\nautomatic_notification: true\n\nThe completion arrives automatically."}
		}
		oldTurn := map[string]any{"kind": "turn", "turnId": "old", "ordinal": 0, "state": "completed", "endedAt": oldEnd, "origin": map[string]any{"kind": "user"},
			"steps": []any{map[string]any{"kind": "step", "stepId": "old.1", "ordinal": 1, "state": "completed", "frames": []any{tool("historical", "task_3", "make old")}}}}
		current := map[string]any{"kind": "turn", "turnId": "current", "ordinal": 1, "state": "running", "origin": map[string]any{"kind": "user"},
			"steps": []any{map[string]any{"kind": "step", "stepId": "current.1", "ordinal": 1, "state": "running", "frames": []any{tool("missed", "task_4", "sleep 100")}}}}
		list := json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"task_2","kind":"shell","status":"running","detached":true,"command":"npm run watch","description":"Watch"},{"task_id":"task_5","kind":"other","status":"running","detached":false}]}`)
		finished := beginNativeKimiReconnect(t, rig, list, nativeKimiRecoveryReset(t, rig.sessionID(), kimiMainAgentID, 20, oldTurn, current))
		waitFor(t, finished, "the reconnect applies the native running list and retained tool output")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the exact native task outcomes finish recovery")
		row, exists := rig.sink.BackgroundTask("session_1/task/main/task_4")
		require.True(t, exists, "a task discovered from retained output opens and closes")
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
		older := kimiRecoveryChild{agentID: "agent-1", callID: "call_background", taskID: "older-background", background: true}
		older.start(t, rig)
		rig.fake.mu.Lock()
		rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
			assert.Equal(t, older.taskID, command.TaskID)
			return older.taskState("ended", "completed")
		}
		rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
			return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": "The earlier background audit completed."}}
		}
		rig.fake.mu.Unlock()
		newer := kimiRecoveryChild{agentID: "agent-9", callID: "call_gone", taskID: "newer-background", background: true}
		newer.start(t, rig)
		unjoined := kimiRecoveryChild{agentID: "agent-10", callID: "unjoined-call", taskID: "unjoined-background", background: true}
		unjoinedState := unjoined.taskState("running", "")
		unjoinedState["description"] = "Unjoined background audit"
		state := newer.taskState("running", "")
		state["description"] = "Background audit"
		list, err := json.Marshal(map[string]any{"type": contracts.KimiEventTaskList, "session_id": rig.sessionID(), "agent_id": kimiMainAgentID, "tasks": []any{state, unjoinedState}})
		require.NoError(t, err)
		turn := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": "running", "origin": map[string]any{"kind": "other"}, "prompt": "Run the background audit.", "steps": []any{}}
		finished := beginNativeKimiReconnect(t, rig, json.RawMessage(list), older.reset(t, rig, "completed", "completed", ""), nativeKimiRecoveryReset(t, rig.sessionID(), newer.agentID, 2, turn), nativeKimiRecoveryReset(t, rig.sessionID(), unjoined.agentID, 1, turn))
		waitFor(t, finished, "the reconnect restores both background child transcripts")
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "the missed background completion is recovered")
		row, child := childOf(t, rig.sink, "session_1/agent-9")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		task, exists := rig.sink.BackgroundTask("session_1/task/main/newer-background")
		require.True(t, exists)
		assert.Equal(t, "Background audit", task.Title)
		assert.Equal(t, []bool{true, true}, child.TurnActives(), "the live turn and its replay both publish the known running state")
		unjoinedRow, unjoinedSink := childOf(t, rig.sink, "session_1/agent-10")
		assert.Equal(t, bgtask.StatusRunning, unjoinedRow.Status)
		assert.Equal(t, "Unjoined background audit", unjoinedRow.Title)
		assert.Equal(t, []bool{true}, unjoinedSink.TurnActives())
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
		assert.Equal(t, []bool{true, false}, endedSink.TurnActives())
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
	finished := beginNativeKimiReconnect(t, rig,
		json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-9","parent_tool_call_id":"call_swarm","swarm_index":1,"description":"Audit #1"}]}`),
		json.RawMessage(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"swarm","toolCallId":"call_swarm","name":"AgentSwarm","state":"running","input":{"description":"Audit the packages"},"agentRefs":[{"agentId":"agent-9","role":"member"}]}}]}}`),
		json.RawMessage(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"agent-9","seq":1,"ops":[{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child-turn","state":"running","origin":{"kind":"other"},"prompt":"Check package A."}}]}}`))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native replay did not reach its registry update")
	}
	assert.False(t, finished(), "receiving frames does not complete a paused registry update")
	releaseRegistration()
	waitFor(t, finished, "the native replay completes its registry update")
	row, child := childOf(t, rig.sink, "session_1/agent-9")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	identity := kimiTranscriptToolIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "swarm", toolCallID: "call_swarm"}
	outcome := invocationOutcomeOf(t, rig.sink, identity, "agent-9")
	assert.Equal(t, bgtask.KindWorkflow, outcome.Kind)
	assert.Equal(t, kimiInvocationGroupKey(identity), outcome.GroupKey)
	assert.Equal(t, "Audit the packages", outcome.GroupLabel)
	assert.Equal(t, "Audit the packages", row.Title)
	nativeTask, exists := rig.sink.BackgroundTask("session_1/task/main/swarm-member")
	require.True(t, exists)
	assert.Equal(t, "Audit #1", nativeTask.Title)
	assert.Equal(t, []bool{true}, child.TurnActives(), "the native header alone publishes running activity")
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

// A complete native reset determines activity. Flat settings alone do not prove a turn ended.
func TestKimiResyncUsesNativeTranscriptActivity(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		items  string
		active bool
	}{
		"an empty complete reset clears the turn":                     {items: `[]`, active: false},
		"a running reset keeps the turn active":                       {items: `[{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"},"steps":[]}]`, active: true},
		"absent transcript restoration keeps the last known activity": {active: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
			var frames []any
			if tc.items != "" {
				frames = append(frames, json.RawMessage(fmt.Sprintf(`{"type":"transcript.reset","session_id":"session_1","payload":{"type":"transcript.reset","agent_id":"main","seq":2,"has_more_older":false,"snapshot":{"items":%s,"interactions":[],"attachments":[],"prompts":[]}}}`, tc.items)))
			}
			finished := beginNativeKimiReconnect(t, rig, frames...)
			waitFor(t, finished, "the native reconnect dispatches its complete replay")
			last, _ := rig.sink.LastTurnActive()
			assert.Equal(t, tc.active, last)
			assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
		})
	}
}

func TestKimiResyncIgnoresASessionItDoesNotDrive(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	before := len(rig.fake.routes())
	rig.agent.resyncSession(context.Background(), "session_other", false)
	rig.agent.resyncSession(context.Background(), "../x", true)
	assert.Len(t, rig.fake.routes(), before, "a session of the past, or an id the server would not issue, sends nothing")
}

// A context clear ends the conversation, and the subagents of the old session
// go with it: what one streamed is closed in its own transcript, and its route
// is gone. Its registry row stays open on purpose (see ClearContext).
func TestKimiRuntimeExitSettlesNativeChildTools(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task"}
	spec.start(t, rig)
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.transcriptAgentOps(t, "agent-0", 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"grep","toolCallId":"call_grep","name":"Grep","state":"running","input":{"pattern":"x"}}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	rig.agent.SetStoppedForTest(true)
	rig.agent.SimulateExitForTest()
	require.NoError(t, rig.agent.Wait())
	messages := child.Messages()
	require.NotEmpty(t, messages)
	var closed bool
	for _, message := range messages {
		if message.SpanID == "session_1/"+kimiTranscriptScope("agent-0", "t0", "t0.1", "grep") && message.Closing {
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
	rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) { return 0, kimiAck{NotFound: sub.IDs} })
	rig.agent.resyncSession(t.Context(), "session_1", false)
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
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			native.transcriptOps(t, 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			native.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child-turn","state":"running","origin":{"kind":"other"},"prompt":"Review the diff."}}`)
			var frames []any
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
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Equal(t, []bool{true}, child.TurnActives())
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

func TestKimiReconcileNativeTurns(t *testing.T) {
	t.Parallel()
	t.Run("a newer native turn replaces the old running turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
		rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{}}}`)
		rig.transcriptReset(t, 3,
			`{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","ordinal":1,"state":"interrupted","frames":[{"kind":"tool","frameId":"call_1","toolCallId":"call_1","name":"Bash","state":"running","input":{}}]}]}`,
			`{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"other","payload":{"kind":"system_trigger"}},"steps":[]}`)
		var closed bool
		for _, message := range rig.sink.Messages() {
			if message.Closing {
				closed = true
				assert.Equal(t, `session_1/kimi/"main"/"t0"/"t0.1"/"call_1"`, message.SpanID)
				assert.Equal(t, agent.MessageCompletionInterrupted, message.Completion)
			}
		}
		assert.True(t, closed)
		assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive())
		rig.agent.dispatchMu.Lock()
		activeTurn := rig.agent.run("main").transcript.activeTurn
		rig.agent.dispatchMu.Unlock()
		assert.Equal(t, "t1", activeTurn)
		rig.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"completed","origin":{"kind":"other","payload":{"kind":"system_trigger"}}}}`)
		assert.False(t, rig.agent.PublishTurnActive().Active)
	})
	t.Run("an empty native snapshot leaves an idle main agent idle", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.transcriptReset(t, 1)
		assert.Equal(t, []bool{false}, rig.sink.TurnActives())
		assert.Zero(t, rig.sink.ResetSpanCount())
	})
}

// A context clear runs while the stream dispatches the old session's events.
// dispatchMu orders the two, so each event either lands before the switch or
// is dropped as another session's, and none reopens a turn afterwards.
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
	replacement.HandleOutput([]byte(`{"type":"todo","session_id":"session_2","agent_id":"main","items":[{"title":"Replacement list","status":"pending"}]}`))
	captured := agent.CaptureTranscript(replacementSink, agent.MessageContent{}, agent.SpanInfo{})
	var frames [][]byte
	for turn := range 50 {
		turnID, stepID := fmt.Sprintf("t%d", turn), fmt.Sprintf("t%d.1", turn)
		operations := []any{
			map[string]any{"op": "turn.upsert", "turn": map[string]any{"kind": "turn", "turnId": turnID, "state": "running", "origin": map[string]any{"kind": "user"}}},
			map[string]any{"op": "frame.upsert", "turnId": turnID, "stepId": stepID, "frame": map[string]any{"kind": "tool", "frameId": "call_1", "toolCallId": "call_1", "name": "Bash", "state": "running", "input": map[string]any{}}},
			map[string]any{"op": "frame.upsert", "turnId": turnID, "stepId": stepID, "frame": map[string]any{"kind": "text", "frameId": "answer", "role": "assistant", "text": "text"}},
			map[string]any{"op": "turn.upsert", "turn": map[string]any{"kind": "turn", "turnId": turnID, "state": "completed", "origin": map[string]any{"kind": "user"}}},
		}
		for _, operation := range operations {
			frame, err := json.Marshal(map[string]any{"type": "transcript.ops", "session_id": "session_1", "payload": map[string]any{"type": "transcript.ops", "agent_id": "main", "seq": len(frames) + 1, "ops": []any{operation}}})
			require.NoError(t, err)
			frames = append(frames, frame)
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

// assertAttachFiltersHistoricalNotices checks the attachment clock through native retention.
// A reset keeps the notice after attachment and omits the earlier notice.
func assertAttachFiltersHistoricalNotices(t *testing.T, rig *kimiTestRig, attach time.Time) {
	t.Helper()
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.transcriptReset(t, 100,
		fmt.Sprintf(`{"kind":"marker","markerId":"old","marker":"notice","at":%q,"payload":{"level":"warning","message":"Historical notice before attachment"}}`, attach.Add(-time.Millisecond).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"kind":"marker","markerId":"new","marker":"notice","at":%q,"payload":{"level":"warning","message":"Fresh notice after attachment"}}`, attach.Add(time.Millisecond).Format(time.RFC3339Nano)))
	rows := rig.sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "Fresh notice after attachment")
	assert.NotContains(t, string(rows[0].Content), "Historical notice before attachment")
}

func TestKimiNewSessionRetainsStateBeforeSubscriptionAcknowledgement(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	a, sink, opts := newConnectedKimiAgent(t, server.URL, agent.Options{})
	fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
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
		return 0, kimiAck{Accepted: []string{sub.SessionID}}
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
	fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
		raw := fmt.Sprintf(`{"type":"goal","session_id":%q,"agent_id":"main","goal":{"objective":"Unaccepted goal","status":"active","turns_used":0,"tokens_used":0,"wall_clock_ms":0}}`, sub.SessionID)
		a.HandleOutput([]byte(raw))
		return 40001, kimiAck{}
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
	rig.agent.resyncSession(t.Context(), rig.sessionID(), true)
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/snapshot")))
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath(rig.sessionID(), "/tasks")))
}
