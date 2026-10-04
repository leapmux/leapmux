package kimi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

		routes := fake.routes()
		status, subscribeReady := -1, len(fake.subscribeFrames()) == 1
		for i, route := range routes {
			if route == "GET "+kimiSessionPath("session_stored", "/status") && status < 0 {
				status = i
			}
		}
		assert.GreaterOrEqual(t, status, 0, "the status read loads the stored session")
		assert.True(t, subscribeReady)

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

	t.Run("a model the catalog lacks is not written", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.store("session_stored", fakeKapSession{Model: "kimi-text", Permission: "manual"})
		connectKimiTestRig(t, fake, server.URL, agent.Options{ResumeSessionID: "session_stored", Options: options(agent.OptionIDModel, "gone")})
		assert.Empty(t, fake.requestsTo("POST "+kimiSessionPath("session_stored", "/profile")))
	})

	t.Run("a session the server does not know fails the resume", func(t *testing.T) {
		t.Parallel()
		fake, server := newFakeKap(t)
		fake.reply("GET "+kimiSessionPath("session_gone", "/status"), fakeKapReply{HTTPStatus: 404, Code: 40401, Msg: "session not found"})
		a, _, opts := newConnectedKimiAgent(t, server.URL, agent.Options{ResumeSessionID: "session_gone"})
		err := a.openStartupSession(opts, 30*time.Second)
		require.ErrorContains(t, err, "session not found")
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
		require.ErrorIs(t, a.openStartupSession(opts, 30*time.Second), errKimiSessionNotLoaded)
	})

	t.Run("an unsafe session id fails before any request", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		_, err := rig.agent.resumeSession(rig.agent.Context(), "../x", kimiSettings{})
		require.ErrorContains(t, err, "session id")
	})
}

func TestKimiClearContext(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDModel, "kimi-text", kimiOptionSwarmMode, kimiSwarmOn)})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_1", "name": contracts.KimiToolBash, "args": map[string]any{}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "half a thought"})
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	rig.feed(t, map[string]any{"type": contracts.KimiEventGoalUpdated, "snapshot": goalSnapshot("active")})

	sessionID, err := rig.agent.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "session_2", sessionID)
	assert.Equal(t, "session_2", rig.sessionID())
	assert.Equal(t, "session_2", rig.sink.LastSessionID())

	profiles := rig.fake.requestsTo("POST " + kimiSessionPath("session_2", "/profile"))
	require.Len(t, profiles, 1)
	var body struct {
		Config map[string]any `json:"agent_config"`
	}
	require.NoError(t, json.Unmarshal(profiles[0].Body, &body))
	assert.Equal(t, "kimi-text", body.Config["model"], "the fresh session keeps the settings")
	assert.Equal(t, true, body.Config["swarm_mode"])

	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionAbort)), 1, "the old session's turn stops working")
	waitFor(t, func() bool { return len(rig.fake.unsubscribeFrames()) == 1 }, "the old session is unsubscribed")
	assert.Equal(t, [][]string{{"session_1"}}, rig.fake.unsubscribeFrames())
	assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls(), "the old session's banner is withdrawn")
	assert.Contains(t, rig.sink.GoalClearSnapshots(), false, "a goal belongs to the old session")
	last, _ := rig.sink.LastTurnActive()
	assert.False(t, last)

	messages := rig.sink.Messages()
	closing := messages[len(messages)-1]
	assert.True(t, closing.Closing, "the open call is closed in the transcript it started in")
	assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)

	// A late event of the old session reaches nothing.
	before := rig.sink.MessageCount()
	rig.agent.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "late"}))
	rig.agent.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "cancelled"}))
	assert.Equal(t, before, rig.sink.MessageCount())
}

func TestKimiClearContextOfAnIdleAgentAbortsNothing(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	_, err := rig.agent.ClearContext()
	require.NoError(t, err)
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionAbort)))
}

func TestKimiClearContextKeepsTheSessionWhenItCannotCreateOne(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.reply("POST "+kimiRouteSessions, fakeKapReply{Code: 50000, Msg: "disk full"})
	_, err := rig.agent.ClearContext()
	require.ErrorContains(t, err, "disk full")
	assert.Equal(t, "session_1", rig.sessionID())
}

func TestKimiCompactContext(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	require.NoError(t, rig.agent.CompactContext())
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionCompact)), 1)

	rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionCompact), fakeKapReply{Code: 40903, Msg: "nothing to compact"})
	require.ErrorContains(t, rig.agent.CompactContext(), "nothing to compact")
}

// A lost socket is re-subscribed from the last durable event, and the server
// replays what the socket missed. It never replays a volatile event, and
// agent.status.updated is one: a plan mode or a swarm mode that changed while
// the socket was down reaches the agent by no replay.
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
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")),
		"the server replayed every durable event, so nothing needs the snapshot")
}

// resyncAfterAGap closes the event socket, and the server answers the
// re-subscribe with resync_required: it kept fewer events than the socket
// missed, and it replays none.
func resyncAfterAGap(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
		return 0, kimiAck{Accepted: sub.IDs, ResyncRequired: sub.IDs}
	})
	rig.fake.closeConnections()
}

// inFlightTurn returns a pointer to a turn id, as fakeKapSession.InFlightTurn takes it.
func inFlightTurn(id int64) *int64 { return &id }

// A server that kept fewer events than the socket missed answers the
// re-subscribe with resync_required, and replays nothing. The snapshot then
// states what the lost events changed.
func TestKimiResyncRestoresWhatTheStreamCouldNotReplay(t *testing.T) {
	t.Parallel()

	t.Run("publishes the requests raised during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.PendingApprovals = []map[string]any{{
				"approval_id": "approval_9", "session_id": "session_1", "agent_id": kimiMainAgentID, "turn_id": 0, "tool_call_id": "call_9",
				"tool_name": contracts.KimiToolBash, "action": "Running: ls", "tool_input_display": map[string]any{"kind": "command", "command": "ls"},
			}}
			session.PendingQuestions = []map[string]any{{
				"question_id": "question_9", "session_id": "session_1", "agent_id": "agent-0",
				"questions": []map[string]any{{"id": "q_0", "question": "Color?", "options": []map[string]any{{"id": "opt_0_0", "label": "Red"}}}},
			}}
		})
		resyncAfterAGap(t, rig)
		waitFor(t, func() bool { return rig.sink.PublishedControlCount() == 2 }, "both requests reach the user")

		byID := map[string]map[string]any{}
		for _, control := range rig.sink.PublishedControls() {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(control.Payload, &payload))
			byID[control.RequestID] = payload
		}
		assert.Equal(t, contracts.KimiEventApprovalRequested, byID["approval_9"]["type"], "the request reads as the event it replaces")
		assert.Equal(t, kimiMainAgentID, byID["approval_9"]["agentId"])
		assert.Equal(t, "session_1", byID["approval_9"]["sessionId"])
		assert.Equal(t, contracts.KimiEventQuestionRequested, byID["question_9"]["type"])
		assert.Equal(t, "agent-0", byID["question_9"]["agentId"])

		require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_9", "allow", nil, nil)), "the user can answer it")
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "approvals", "approval_9", "")), 1)
	})

	t.Run("withdraws a request the server resolved during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.feed(t, approvalEvent("approval_2", map[string]any{"kind": "command", "command": "pwd"}))
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.PendingApprovals = []map[string]any{{"approval_id": "approval_2", "session_id": "session_1", "agent_id": kimiMainAgentID,
				"tool_name": contracts.KimiToolBash, "tool_input_display": map[string]any{"kind": "command", "command": "pwd"}}}
		})
		resyncAfterAGap(t, rig)
		waitFor(t, func() bool { return len(rig.sink.CanceledControls()) == 1 }, "the resolved request leaves the banner")
		assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
		assert.Equal(t, 2, rig.sink.PublishedControlCount(), "a request still pending is not published twice")
		require.ErrorIs(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil)), errKimiControlGone)
	})

	// The status's `busy` is true while ANY agent of the session runs, or a
	// background task does, so it cannot state the main agent's turn. The
	// snapshot's in-flight turn is the main agent's alone.
	t.Run("clears a main turn that ended during the gap, while a background task keeps the session busy", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_1", "name": contracts.KimiToolBash, "args": map[string]any{}})
		rig.fake.setBusy("session_1", true)
		resyncAfterAGap(t, rig)

		waitFor(t, func() bool {
			last, _ := rig.sink.LastTurnActive()
			return !last
		}, "a turn the stream saw start and never saw end must not latch the agent busy")
		messages := rig.sink.Messages()
		assert.True(t, messages[len(messages)-1].Closing, "the call the ended turn left open is closed")
		assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
		rig.fake.setBusy("session_1", false)
		require.NoError(t, rig.agent.SendInput("Next.", nil))
	})

	t.Run("starts a main turn that started during the gap", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.Busy = true
			session.InFlightTurn = inFlightTurn(3)
		})
		resyncAfterAGap(t, rig)

		waitFor(t, func() bool {
			last, _ := rig.sink.LastTurnActive()
			return last
		}, "the running turn holds the input queue")
		assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(),
			"the snapshot does not state who started the turn, so it takes no steer")
		var busy *agent.AgentBusyError
		require.ErrorAs(t, rig.agent.SendInput("Next.", nil), &busy)

		// The turn's own end arrives live, and it ends the turn.
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 3, "reason": contracts.KimiTurnEndCompleted})
		last, _ := rig.sink.LastTurnActive()
		assert.False(t, last)
	})

	t.Run("keeps a main turn that still runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.fake.update("session_1", func(session *fakeKapSession) { session.InFlightTurn = inFlightTurn(0) })
		resyncAfterAGap(t, rig)
		waitFor(t, func() bool { return len(rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot"))) == 1 }, "the resync reads the snapshot")
		assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive(), "the user's turn keeps its steer")
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
			return 0, kimiAck{Accepted: sub.IDs, Cursors: map[string]kimiCursor{"session_1": {Epoch: fakeKapEpoch}}}
		})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.PendingApprovals = []map[string]any{{"approval_id": "approval_9", "session_id": "session_1", "agent_id": kimiMainAgentID,
				"tool_name": contracts.KimiToolBash, "tool_input_display": map[string]any{"kind": "command", "command": "ls"}}}
		})
		rig.fake.closeConnections()
		waitFor(t, func() bool { return rig.sink.PublishedControlCount() == 1 }, "the dropped session's request reaches the user")
		subscribes := rig.fake.subscribeFrames()
		require.Len(t, subscribes, 3, "the first subscribe, the one the server refused, and the one after the load")
		rig.agent.stream.mu.Lock()
		_, tracked := rig.agent.stream.sessions["session_1"]
		rig.agent.stream.mu.Unlock()
		assert.True(t, tracked, "the next reconnect re-subscribes the session again")
	})
}

// kimiRosterEntry is one entry of the snapshot's subagent roster, as
// SubagentRosterTracker builds it.
func kimiRosterEntry(id, status, phase, parentCall string, extra map[string]any) map[string]any {
	entry := map[string]any{
		"id": id, "session_id": "session_1", "kind": "subagent", "description": "Reviewer", "status": status,
		"subagent_phase": phase, "subagent_type": "explore", "parent_tool_call_id": parentCall,
		"run_in_background": false, "created_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	for key, value := range extra {
		entry[key] = value
	}
	return entry
}

// kimiTaskEntry is one item of GET .../tasks, as toWireTask builds it.
func kimiTaskEntry(id, kind, status string, startedAt time.Time, extra map[string]any) map[string]any {
	started := startedAt.UTC().Format(time.RFC3339Nano)
	entry := map[string]any{
		"id": id, "session_id": "session_1", "kind": kind, "description": id, "status": status,
		"created_at": started, "started_at": started, "run_in_background": true,
	}
	for key, value := range extra {
		entry[key] = value
	}
	return entry
}

// A gap the server cannot replay loses the subagent and task events too. The
// snapshot's roster states the foreground and swarm subagents of the current
// main turn, and GET .../tasks states the main agent's tasks.
func TestKimiResyncRestoresSubagentsAndTasks(t *testing.T) {
	t.Parallel()

	rowStatus := func(rig *kimiTestRig, rowKey string) func() bool {
		return func() bool {
			row, ok := rig.sink.BackgroundTask(rowKey)
			return ok && row.Status.IsFinished()
		}
	}

	// The text that the gap cut ends as a failed turn's does when the subagent
	// failed, and as interrupted otherwise.
	for name, tc := range map[string]struct {
		wire       string
		preview    string
		status     bgtask.Status
		completion agent.MessageCompletion
	}{
		"completed": {wire: "completed", preview: "All good.", status: bgtask.StatusCompleted, completion: agent.MessageCompletionInterrupted},
		"failed":    {wire: "failed", preview: "provider exploded", status: bgtask.StatusFailed, completion: agent.MessageCompletionError},
		"cancelled": {wire: "cancelled", status: bgtask.StatusStopped, completion: agent.MessageCompletionInterrupted},
	} {
		t.Run("a subagent that "+name+" during the gap closes with the status the server reports", func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
			spawnAgent(t, rig, "agent-0", "call_child", nil)
			rig.fake.update("session_1", func(session *fakeKapSession) {
				session.InFlightTurn = inFlightTurn(0)
				session.Subagents = []map[string]any{kimiRosterEntry("agent-0", tc.wire, tc.wire, "call_child", map[string]any{"output_preview": tc.preview})}
			})
			resyncAfterAGap(t, rig)

			waitFor(t, rowStatus(rig, "session_1/agent-0"), "the row closes")
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, tc.status, row.Status)
			assert.Equal(t, []bool{true, false}, child.TurnActives(), "the child's tab stops showing the turn the gap ended")
			var text bool
			for _, message := range child.Messages() {
				var row map[string]string
				if json.Unmarshal(message.Content, &row) == nil && row[contracts.AssembledMessageFieldText] == "Found it." {
					text = true
					assert.Equal(t, string(tc.completion), row[contracts.AssembledMessageFieldCompletion],
						"the gap cut the rest of the text")
				}
			}
			assert.True(t, text, "the text the subagent streamed before the gap is kept")
			if tc.status == bgtask.StatusCompleted {
				assert.NotEmpty(t, child.LeapMuxNotifications(), "the report the server states reaches the child transcript")
			}
			if tc.status == bgtask.StatusFailed {
				last := child.Messages()[len(child.Messages())-1]
				_, text := assembledRow(t, last)
				assert.Equal(t, "provider exploded", text, "the error the server states reaches the child transcript")
			}
			last, _ := rig.sink.LastTurnActive()
			assert.True(t, last, "the main turn still runs")
		})
	}

	t.Run("a swarm member that started during the gap opens", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_swarm", "name": contracts.KimiToolAgentSwarm,
			"args": map[string]any{"description": "Audit the packages"}})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.InFlightTurn = inFlightTurn(0)
			session.Subagents = []map[string]any{kimiRosterEntry("agent-1", "running", "working", "call_swarm",
				map[string]any{"description": "Audit #1", "swarm_index": 1})}
		})
		resyncAfterAGap(t, rig)

		waitFor(t, func() bool { _, ok := rig.sink.BackgroundTask("session_1/agent-1"); return ok }, "the member's row opens")
		row, child := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.Equal(t, bgtask.KindWorkflow, row.Kind)
		assert.Equal(t, "session_1/call_swarm", row.GroupKey)
		assert.Equal(t, "Audit the packages", row.GroupLabel)
		assert.Equal(t, "Audit #1", row.Title)
		waitFor(t, func() bool { return len(child.TurnActives()) == 1 }, "the member's tab shows its running turn")
		assert.Equal(t, []bool{true}, child.TurnActives())
	})

	t.Run("a subagent that started and ended during the gap opens and closes", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.InFlightTurn = inFlightTurn(0)
			session.Subagents = []map[string]any{kimiRosterEntry("agent-2", "completed", "completed", "call_gone", map[string]any{"output_preview": "Done."})}
		})
		resyncAfterAGap(t, rig)

		waitFor(t, rowStatus(rig, "session_1/agent-2"), "the row opens and closes")
		row, _ := childOf(t, rig.sink, "session_1/agent-2")
		assert.Equal(t, bgtask.StatusCompleted, row.Status)
		assert.Equal(t, bgtask.KindSubagent, row.Kind)
	})

	t.Run("a foreground subagent that no source lists any more reads Interrupted", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		spawnAgent(t, rig, "agent-1", "call_background", map[string]any{"runInBackground": true})
		// A new main turn started during the gap, and the server cleared the roster
		// with it. A foreground subagent ends before its main turn does.
		rig.fake.update("session_1", func(session *fakeKapSession) { session.InFlightTurn = inFlightTurn(1) })
		resyncAfterAGap(t, rig)

		waitFor(t, rowStatus(rig, "session_1/agent-0"), "the row of the subagent that ended closes")
		row, child := childOf(t, rig.sink, "session_1/agent-0")
		assert.Equal(t, bgtask.StatusInterrupted, row.Status, "the server keeps no status for it")
		assert.Equal(t, []bool{true, false}, child.TurnActives())
		background, _ := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusRunning, background.Status,
			"the main agent's task list states no status for this background subagent, so its row keeps its state")
	})

	t.Run("a subagent's turn follows the phase the roster states", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_swarm", "name": contracts.KimiToolAgentSwarm,
			"args": map[string]any{"description": "Audit the packages"}})
		for i, id := range []string{"agent-0", "agent-1"} {
			rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": id, "subagentName": "explore",
				"parentToolCallId": "call_swarm", "parentAgentId": kimiMainAgentID, "description": id, "swarmIndex": i + 1})
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": id, "turnId": 0,
				"origin": map[string]any{"kind": contracts.KimiOriginSystemTrigger}, "prompt": "Check it."})
		}
		// agent-1's first turn ended; the swarm retries it during the gap.
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": "agent-1", "turnId": 0, "reason": contracts.KimiTurnEndFailed})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.InFlightTurn = inFlightTurn(0)
			session.Subagents = []map[string]any{
				kimiRosterEntry("agent-0", "running", "suspended", "call_swarm", map[string]any{"swarm_index": 1}),
				kimiRosterEntry("agent-1", "running", "working", "call_swarm", map[string]any{"swarm_index": 2}),
			}
		})
		resyncAfterAGap(t, rig)

		_, suspended := childOf(t, rig.sink, "session_1/agent-0")
		_, working := childOf(t, rig.sink, "session_1/agent-1")
		waitFor(t, func() bool { return len(suspended.TurnActives()) == 2 && len(working.TurnActives()) == 3 }, "both turn flags follow the roster")
		assert.Equal(t, []bool{true, false}, suspended.TurnActives(), "a suspended member runs no turn")
		assert.Equal(t, []bool{true, false, true}, working.TurnActives(), "a working member runs one")
		for _, rowKey := range []string{"session_1/agent-0", "session_1/agent-1"} {
			row, _ := rig.sink.BackgroundTask(rowKey)
			assert.Equal(t, bgtask.StatusRunning, row.Status, rowKey)
		}
	})

	t.Run("a background task that the gap started or ended", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTaskStarted,
			"info": map[string]any{"taskId": "task_1", "kind": "process", "command": "npm run build", "status": "running"}})
		now := time.Now()
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.Tasks = []map[string]any{
				kimiTaskEntry("task_1", "bash", "failed", now.Add(-time.Second), map[string]any{"command": "npm run build"}),
				kimiTaskEntry("task_2", "bash", "running", now, map[string]any{"command": "npm run watch", "description": "Watch"}),
				kimiTaskEntry("task_3", "bash", "completed", now.Add(-time.Hour), map[string]any{"command": "make old"}),
				kimiTaskEntry("task_4", "bash", "cancelled", now, map[string]any{"command": "sleep 100"}),
				kimiTaskEntry("task_5", "tool", "running", now, nil),
			}
		})
		resyncAfterAGap(t, rig)

		waitFor(t, rowStatus(rig, "session_1/task/task_4"), "a task that started and ended during the gap opens and closes")
		row, _ := rig.sink.BackgroundTask("session_1/task/task_4")
		assert.Equal(t, bgtask.StatusStopped, row.Status, "the server's cancelled is a stop")
		row, _ = rig.sink.BackgroundTask("session_1/task/task_1")
		assert.Equal(t, bgtask.StatusFailed, row.Status, "a task that ended during the gap closes with the server's status")
		row, ok := rig.sink.BackgroundTask("session_1/task/task_2")
		require.True(t, ok, "a task that started during the gap opens")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.Equal(t, bgtask.KindShell, row.Kind)
		assert.Equal(t, "npm run watch", row.Title)
		_, ok = rig.sink.BackgroundTask("session_1/task/task_3")
		assert.False(t, ok, "a task that started before the agent attached is the session's history, not the gap's")
		_, ok = rig.sink.BackgroundTask("session_1/task/task_5")
		assert.False(t, ok, "a question task gets no row")
	})

	t.Run("a background subagent that the task list states", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		spawnAgent(t, rig, "agent-1", "call_background", map[string]any{"runInBackground": true})
		now := time.Now()
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.InFlightTurn = inFlightTurn(0)
			session.Tasks = []map[string]any{
				kimiTaskEntry("agent-task-agent-1", "subagent", "completed", now.Add(-time.Second), map[string]any{"agent_id": "agent-1"}),
				kimiTaskEntry("agent-task-9", "subagent", "running", now, map[string]any{
					"agent_id": "agent-9", "subagent_type": "explore", "description": "Background audit", "parent_tool_call_id": "call_gone",
				}),
			}
		})
		resyncAfterAGap(t, rig)

		waitFor(t, func() bool { _, ok := rig.sink.BackgroundTask("session_1/agent-9"); return ok }, "the subagent that started during the gap opens")
		row, _ := childOf(t, rig.sink, "session_1/agent-9")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.Equal(t, "Background audit", row.Title)
		require.NoError(t, rig.agent.InterruptChild("session_1/agent-9"), "its task is what stops it")
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "tasks", "agent-task-9", kimiActionCancel)), 1)
		ended, _ := childOf(t, rig.sink, "session_1/agent-1")
		assert.Equal(t, bgtask.StatusCompleted, ended.Status, "the background subagent that ended during the gap closes")
	})

	t.Run("a subagent in a follow-up turn of its own is left to that turn", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, map[string]any{"resultSummary": "First run."})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": "agent-0", "turnId": 1, "origin": map[string]any{"kind": "user"}, "prompt": "More."})
		rig.fake.update("session_1", func(session *fakeKapSession) {
			session.InFlightTurn = inFlightTurn(0)
			session.Subagents = []map[string]any{
				kimiRosterEntry("agent-0", "completed", "completed", "call_child", map[string]any{"output_preview": "First run."}),
				// The roster lists agent-2 after agent-0, so its closed row shows that
				// the resync reached agent-0 already.
				kimiRosterEntry("agent-2", "completed", "completed", "call_gone", nil),
			}
		})
		resyncAfterAGap(t, rig)
		waitFor(t, rowStatus(rig, "session_1/agent-2"), "the resync reads the whole roster")
		row, _ := rig.sink.BackgroundTask("session_1/agent-0")
		assert.Equal(t, bgtask.StatusRunning, row.Status, "the roster states the run before the follow-up turn, and that turn's own end closes the row")
	})
}

// A snapshot that cannot be read leaves the status, whose `busy` is false only
// when no agent of the session runs. That is enough to clear a main turn that
// ended during the gap, and not enough to start one.
func TestKimiResyncWithoutASnapshot(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		busy   bool
		active bool
	}{
		"an idle session clears the turn":     {busy: false, active: false},
		"a busy session keeps the turn as is": {busy: true, active: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
			rig.fake.setBusy("session_1", tc.busy)
			rig.fake.reply("GET "+kimiSessionPath("session_1", "/snapshot"), fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "internal error"})
			rig.agent.resyncSession(context.Background(), "session_1", false)
			last, _ := rig.sink.LastTurnActive()
			assert.Equal(t, tc.active, last)
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

// A context clear that cannot open the fresh session keeps the one the agent
// drives, whichever step fails.
func TestKimiClearContextKeepsTheSessionWhenTheFreshOneFails(t *testing.T) {
	t.Parallel()

	t.Run("its profile write", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath("session_2", "/profile"), fakeKapReply{Code: 50000, Msg: "profile locked"})
		_, err := rig.agent.ClearContext()
		require.ErrorContains(t, err, "configure the Kimi Code session")
		assert.Contains(t, err.Error(), "profile locked")
		assert.Equal(t, "session_1", rig.sessionID())
		assert.NotContains(t, rig.sink.SessionIDs(), "session_2")
	})

	t.Run("its subscribe", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			if sub.IDs[0] == "session_2" {
				return 0, kimiAck{NotFound: sub.IDs}
			}
			return rig.fake.defaultAck(sub)
		})
		_, err := rig.agent.ClearContext()
		require.ErrorContains(t, err, "subscribe to the Kimi Code session")
		assert.Equal(t, "session_1", rig.sessionID())
		rig.agent.stream.mu.Lock()
		_, fresh := rig.agent.stream.sessions["session_2"]
		_, current := rig.agent.stream.sessions["session_1"]
		rig.agent.stream.mu.Unlock()
		assert.False(t, fresh, "a reconnect does not subscribe the session the clear gave up")
		assert.True(t, current)
	})
}

func TestKimiClearContextOfABusyAgentSucceedsWhenTheAbortFails(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
	rig.fake.reply("POST "+kimiSessionPath("session_1", kimiActionAbort), fakeKapReply{Code: 50000, Msg: "abort failed"})
	sessionID, err := rig.agent.ClearContext()
	require.NoError(t, err, "the fresh session is open, and the old turn is the server's to end")
	assert.Equal(t, "session_2", sessionID)
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", kimiActionAbort)), 1)
}

// A context clear ends the conversation, and the subagents of the old session
// go with it: what one streamed is closed in its own transcript, and its route
// is gone. Its registry row stays open on purpose (see ClearContext).
func TestKimiClearContextSettlesTheSubagentsOfTheOldSession(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "agentId": "agent-0", "turnId": 0, "toolCallId": "call_grep",
		"name": contracts.KimiToolGrep, "args": map[string]any{"pattern": "x"}})
	row, child := childOf(t, rig.sink, "session_1/agent-0")

	_, err := rig.agent.ClearContext()
	require.NoError(t, err)

	messages := child.Messages()
	closing := messages[len(messages)-1]
	assert.Equal(t, kimiSpanID("session_1", "agent-0", 0, "call_grep"), closing.SpanID)
	assert.True(t, closing.Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion, "the subagent's open call is closed where it started")
	require.ErrorIs(t, rig.agent.InterruptChild("session_1/agent-0"), errKimiChildUnknown, "the old session's subagent has no route")
	after, _ := rig.sink.BackgroundTask("session_1/agent-0")
	assert.Equal(t, row.Status, after.Status)
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

func TestKimiResyncStopsWhenItCannotLoadTheSession(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.reply("GET "+kimiSessionPath("session_1", "/status"), fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "status down"})
	rig.agent.resyncSession(context.Background(), "session_1", false)
	assert.Len(t, rig.fake.subscribeFrames(), 1, "a session that no read loaded takes no subscribe")
	assert.Empty(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/snapshot")))
}

// The task list is what proves that a foreground subagent ended: the roster
// forgets every entry when a main turn starts. A task list that could not be
// read proves nothing, so no row closes for it.
func TestKimiResyncWithoutTheTaskListClosesNoUnlistedSubagent(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	rig.fake.update("session_1", func(session *fakeKapSession) { session.InFlightTurn = inFlightTurn(1) })
	rig.fake.reply("GET "+kimiSessionPath("session_1", "/tasks"), fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "tasks down"})

	rig.agent.resyncSession(context.Background(), "session_1", false)
	assert.Len(t, rig.fake.requestsTo("GET "+kimiSessionPath("session_1", "/tasks")), 1)
	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []bool{true}, child.TurnActives(), "the subagent's turn stays as the stream left it")
	last, _ := rig.sink.LastTurnActive()
	assert.True(t, last, "the snapshot still states the main turn")
}

func TestKimiReconcileTurn(t *testing.T) {
	t.Parallel()

	t.Run("another turn that runs replaces the one the stream saw", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_1", "name": contracts.KimiToolBash, "args": map[string]any{}})

		rig.agent.reconcileTurn(&kimiInFlightTurn{TurnID: 1})
		messages := rig.sink.Messages()
		closing := messages[len(messages)-1]
		assert.Equal(t, kimiSpanID("session_1", kimiMainAgentID, 0, "call_1"), closing.SpanID)
		assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion, "the turn that ended in the gap leaves nothing running")
		assert.Equal(t, 1, rig.sink.ResetSpanCount())
		assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive(),
			"the snapshot does not state who started the new turn, so it takes no steer")
		rig.agent.Mu.Lock()
		turnID := rig.agent.runs[kimiMainAgentID].turnID
		rig.agent.Mu.Unlock()
		assert.EqualValues(t, 1, turnID, "the new turn's own end ends it")
	})

	t.Run("an idle agent that the snapshot states idle publishes nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.reconcileTurn(nil)
		assert.Empty(t, rig.sink.TurnActives())
		assert.Zero(t, rig.sink.ResetSpanCount())
	})
}

// A context clear runs while the stream dispatches the old session's events.
// dispatchMu orders the two, so each event either lands before the switch or
// is dropped as another session's, and none reopens a turn afterwards.
func TestKimiClearContextWhileEventsArrive(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	var frames [][]byte
	for turn := range 50 {
		for _, payload := range []map[string]any{
			{"type": contracts.KimiEventTurnStarted, "turnId": turn, "origin": map[string]any{"kind": "user"}},
			{"type": contracts.KimiEventToolCallStarted, "turnId": turn, "toolCallId": "call_1", "name": contracts.KimiToolBash, "args": map[string]any{}},
			{"type": contracts.KimiEventAssistantDelta, "turnId": turn, "delta": "text"},
		} {
			frames = append(frames, kimiEventFrame(t, "session_1", payload))
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for _, frame := range frames {
			rig.agent.HandleOutput(frame)
		}
	})
	sessionID, err := rig.agent.ClearContext()
	wg.Wait()
	require.NoError(t, err)
	assert.Equal(t, "session_2", sessionID)
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive(), "no event of the old session reopens a turn in the new one")
	rig.agent.Mu.Lock()
	for agentID, run := range rig.agent.runs {
		assert.Empty(t, run.tools, "the new session holds no call of the old one: %s", agentID)
	}
	rig.agent.Mu.Unlock()
}

// assertAttachSplitsTheTaskHistory checks that the agent attached to its
// current session at attach. It reports two tasks around that time after a gap:
// the one from before is the session's history and opens no row, and the one
// from after opens. A stamp from any other clock puts both tasks on one side.
func assertAttachSplitsTheTaskHistory(t *testing.T, rig *kimiTestRig, attach time.Time) {
	t.Helper()
	sessionID := rig.sessionID()
	rig.fake.update(sessionID, func(session *fakeKapSession) {
		session.Tasks = []map[string]any{
			kimiTaskEntry("task_old", "bash", "running", attach.Add(-time.Millisecond), map[string]any{"session_id": sessionID, "command": "make old"}),
			kimiTaskEntry("task_new", "bash", "running", attach.Add(time.Millisecond), map[string]any{"session_id": sessionID, "command": "make new"}),
		}
	})
	resyncAfterAGap(t, rig)
	newKey := kimiTaskRowKey(sessionID, "task_new")
	waitFor(t, func() bool {
		_, ok := rig.sink.BackgroundTask(newKey)
		return ok
	}, "a task from after the attach opens")
	_, ok := rig.sink.BackgroundTask(kimiTaskRowKey(sessionID, "task_old"))
	assert.False(t, ok, "a task from before the attach is the session's history")
}

// ClearContext stamps its attach to the fresh session from the agent's clock.
func TestKimiClearContextStampsTheAttachFromTheClock(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	attach := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	clock.Set(attach).MustWait(ctx)
	rig.agent.clock = clock

	sessionID, err := rig.agent.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session_2", sessionID)
	assertAttachSplitsTheTaskHistory(t, rig, attach)
}
