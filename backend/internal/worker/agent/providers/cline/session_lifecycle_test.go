package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestANewSessionIsSubscribedAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	assert.NotEmpty(t, r.sessionID())
	frames := r.hub.subscribeFrames()
	require.Len(t, frames, 1)
	assert.Equal(t, r.sessionID(), frames[0].SessionID)
	create := r.hub.commandsNamed(commandSessionCreate)[0]
	var metadata map[string]any
	require.True(t, create.field("metadata", &metadata))
	assert.Equal(t, map[string]any{"source": sessionSource, "interactive": true}, metadata)
	assert.Equal(t, r.agent.opts.WorkingDir, create.str("cwd"))
	assert.Equal(t, r.agent.workspaceRoot, create.str("workspaceRoot"))
}

func TestAResumeCreatesTheSessionUnderItsStoredID(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	stored := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<user_input mode=\"act\">Hi</user_input>"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Hello."}}},
	}
	r.hub.store("stored-1", stored)
	sessionID, err := r.agent.openSession(context.Background(), r.agent.settings, "stored-1")
	require.NoError(t, err)
	t.Cleanup(func() { releaseSession(sessionID, r.agent.AgentID()) })
	assert.Equal(t, "stored-1", sessionID)
	create := r.hub.commandsNamed(commandSessionCreate)[0]
	var config map[string]any
	require.True(t, create.field("sessionConfig", &config))
	assert.Equal(t, "stored-1", config["sessionId"])
	var messages []any
	require.True(t, create.field("initialMessages", &messages))
	assert.Len(t, messages, 2)
}

func TestAResumeOfAnUnknownSessionFails(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	_, err := r.agent.openSession(context.Background(), r.agent.settings, "missing-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-1")
	assert.Contains(t, err.Error(), "not in Cline's session store", "a session that Cline does not know reads as such, not as a failed read")
	assert.Empty(t, r.hub.commandsNamed(commandSessionCreate))
	require.NoError(t, claimSession("missing-1", "someone"), "a failed resume releases its claim")
	releaseSession("missing-1", "someone")
}

// A subscription that fails leaves no claim behind, for a new session and for a
// resumed one.
func TestAFailedSubscriptionReleasesTheClaim(t *testing.T) {
	t.Parallel()
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.noSession = true })
			// Unique in the test binary: the claims are process-wide.
			sessionID := "sub-fail-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
			resumeID := ""
			if resume {
				resumeID = sessionID
				r.hub.store(resumeID, []any{map[string]any{"role": "user", "content": "Hi."}})
			}
			r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
				return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": sessionID}}}
			})
			r.agent.subscribe = func(context.Context, string) error { return errHubConnectionLost }
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := r.agent.openSession(ctx, r.agent.settings, resumeID)
			require.ErrorIs(t, err, errHubConnectionLost)
			require.Len(t, r.hub.commandsNamed(commandSessionCreate), 1)
			require.NoError(t, claimSession(sessionID, "other-agent"), "the failed open released its claim")
			releaseSession(sessionID, "other-agent")
			assert.Empty(t, r.agent.claims)
		})
	}
}

// Cline records the process that holds a session and whether it is resident.
// A resume of a session that another Cline process still holds would give two
// processes the session's files, and each rewrites them whole.
func TestAResumeRefusesASessionThatAnotherClineProcessHolds(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"idle", "running", "pending"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.noSession = true })
			sessionID := "held-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
			r.hub.store(sessionID, []any{map[string]any{"role": "user", "content": "Hi."}})
			// This test process runs, and it is no daemon of the agent.
			r.hub.storeRecord(sessionID, status, os.Getpid())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := r.agent.openSession(ctx, r.agent.settings, sessionID)
			require.ErrorIs(t, err, errSessionHeldElsewhere)
			assert.Contains(t, err.Error(), fmt.Sprintf("Cline process %d holds this session", os.Getpid()))
			assert.Contains(t, err.Error(), "Close it in Cline, then resume it here")
			assert.Empty(t, r.hub.commandsNamed(commandSessionCreate), "no second runtime of the session starts")
			require.NoError(t, claimSession(sessionID, "other-agent"), "the refusal released its claim")
			releaseSession(sessionID, "other-agent")
		})
	}
}

func TestAResumeTakesASessionThatNoOtherProcessHolds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status string
		pid    func(t *testing.T, r *rig) int
	}{
		{"a final status", "completed", func(*testing.T, *rig) int { return os.Getpid() }},
		{"a failed run", "failed", func(*testing.T, *rig) int { return os.Getpid() }},
		{"an aborted run", "aborted", func(*testing.T, *rig) int { return os.Getpid() }},
		{"a process that exited", "idle", func(t *testing.T, _ *rig) int { return endedProcessPID(t) }},
		{"no process", "idle", func(*testing.T, *rig) int { return 0 }},
		{"the agent's own daemon", "running", func(_ *testing.T, r *rig) int {
			// A process that runs, which the agent's record states as its daemon.
			r.agent.record.PID = os.Getpid()
			return r.agent.record.PID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.noSession = true })
			sessionID := "free-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
			r.hub.store(sessionID, []any{map[string]any{"role": "user", "content": "Hi."}})
			r.hub.storeRecord(sessionID, tc.status, tc.pid(t, r))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			opened, err := r.agent.openSession(ctx, r.agent.settings, sessionID)
			// The stop at the test's end waits for the recorded daemon to exit and
			// kills it then, so the record must not state the test process.
			r.agent.record.PID = 0
			require.NoError(t, err)
			assert.Equal(t, sessionID, opened)
			r.agent.releaseClaims()
		})
	}
}

func TestTwoAgentsCannotRunOneSession(t *testing.T) {
	t.Parallel()
	first := newRig(t, func(c *rigConfig) { c.noSession = true })
	second := newRig(t, func(c *rigConfig) {
		c.noSession = true
		c.opts.AgentID = "other-agent"
	})
	for _, r := range []*rig{first, second} {
		r.hub.store("shared-1", []any{})
	}
	sessionID, err := first.agent.openSession(context.Background(), first.agent.settings, "shared-1")
	require.NoError(t, err)
	_, err = second.agent.openSession(context.Background(), second.agent.settings, "shared-1")
	require.ErrorIs(t, err, errSessionHosted)
	assert.Empty(t, second.hub.commandsNamed(commandSessionCreate), "the second agent reads nothing")

	releaseSession(sessionID, first.agent.AgentID())
	sessionID, err = second.agent.openSession(context.Background(), second.agent.settings, "shared-1")
	require.NoError(t, err)
	releaseSession(sessionID, second.agent.AgentID())
}

func TestClaimSession(t *testing.T) {
	t.Parallel()
	require.NoError(t, claimSession("claim-1", "a"))
	require.NoError(t, claimSession("claim-1", "a"), "a second claim of the same agent holds")
	require.ErrorIs(t, claimSession("claim-1", "b"), errSessionHosted)
	releaseSession("claim-1", "b")
	require.ErrorIs(t, claimSession("claim-1", "b"), errSessionHosted, "only the owner releases")
	releaseSession("claim-1", "a")
	require.NoError(t, claimSession("claim-1", "b"))
	releaseSession("claim-1", "b")
	releaseSession("", "b")
}

func TestClearContextOpensANewSession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	old := r.sessionID()
	r.startTurn(t, "Hello.")
	r.emit(contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the approval is published")

	sessionID, err := r.agent.ClearContext()
	require.NoError(t, err)
	assert.NotEqual(t, old, sessionID)
	assert.Equal(t, sessionID, r.sessionID())
	assert.Equal(t, sessionID, r.sink.LastSessionID())
	assert.False(t, r.turnActive(), "the old turn is over")
	assert.Equal(t, []string{"a1"}, r.sink.CanceledControls())
	aborts := r.hub.commandsNamed(commandRunAbort)
	require.Len(t, aborts, 1)
	assert.Equal(t, old, aborts[0].str("sessionId"))
	detaches := r.hub.commandsNamed(commandSessionDetach)
	require.Len(t, detaches, 1)
	assert.Equal(t, old, detaches[0].str("sessionId"))
	waitFor(t, func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return r.hub.subscriptions[sessionID] && !r.hub.subscriptions[old]
	}, "the stream moves to the new session")
	require.NoError(t, claimSession(old, "someone"), "the old session is released")
	releaseSession(old, "someone")
}

func TestContributionsFollowTheMode(t *testing.T) {
	t.Parallel()
	act := contributions(sessionModeAct)
	require.Len(t, act, 1)
	assert.Equal(t, contracts.ClineCapabilityAskQuestion, act[0]["capabilityName"])
	plan := contributions(sessionModePlan)
	require.Len(t, plan, 2)
	assert.Equal(t, capabilitySwitchToActMode, plan[1]["capabilityName"])
	assert.Equal(t, map[string]any{"completesRun": true}, plan[1]["lifecycle"])
}

func TestAnApprovedPlanContinuesInActMode(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	sessionID := r.sessionID()
	r.hub.store(sessionID, []any{})
	requestID := r.startTurn(t, "Go ahead.")
	r.emit(contracts.ClineEventAssistantFinished, map[string]any{"text": "Plan: edit it."})
	r.emit(contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the plan approval is published")

	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{
		ApprovalId: "p1", Approved: true, PermissionMode: contracts.ClinePermissionModeAutoApprove,
	})))
	replies := approvalReplies(t, r.hub)
	require.Len(t, replies, 1)
	assert.Empty(t, replies[0].PermissionMode, "Cline never sees LeapMux's field")

	tool := question(r, "tool-1")
	tool["capabilityName"] = capabilitySwitchToActMode
	tool["payload"] = map[string]any{"toolName": contracts.ClineToolSwitchToActMode, "input": map[string]any{}}
	r.emit(contracts.ClineEventCapabilityRequested, tool)
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandCapabilityRespond)) == 1 }, "the plan tool runs")
	var answer contracts.ClineCapabilityReply
	require.NoError(t, json.Unmarshal(r.hub.commandsNamed(commandCapabilityRespond)[0].Payload, &answer))
	assert.True(t, answer.Ok)
	assert.JSONEq(t, `{"result":"`+switchToActModeResult+`"}`, string(answer.Payload))

	r.emit(contracts.ClineEventRunCompleted, map[string]any{"reason": "completed"})
	r.hub.reply(requestID, fakeReply{})
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionSendInput)) == 2 }, "the worker asks the model to go on")
	sends := r.hub.commandsNamed(commandSessionSendInput)
	assert.Equal(t, actModeContinuationPrompt, sends[1].str("prompt"))
	assert.Equal(t, sessionModeAct, sends[1].str("mode"))
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2, "the session is rebuilt in Act mode before the prompt")
	var runtime map[string]any
	require.True(t, creates[1].field("runtimeOptions", &runtime))
	assert.Equal(t, sessionModeAct, runtime["mode"])
	assert.Equal(t, contracts.ClinePermissionModeAutoApprove, r.agent.settings.permissionMode)
	assert.True(t, r.turnActive(), "the continuation is a turn")
	assert.Contains(t, r.sink.PermissionModes(), contracts.ClinePermissionModeAutoApprove)
}

func TestARejectedPlanStaysInPlanMode(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	requestID := r.startTurn(t, "Plan it.")
	r.emit(contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the plan approval is published")
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "p1", Reason: "Split it."})))
	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1, "no rebuild")
	assert.Equal(t, contracts.ClinePermissionModePlan, r.agent.settings.permissionMode)
}

func TestAPlanApprovedAndThenInterruptedSwitchesWithoutGoingOn(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	r.hub.store(r.sessionID(), []any{})
	requestID := r.startTurn(t, "Go.")
	r.emit(contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the plan approval is published")
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "p1", Approved: true})))
	r.emit(contracts.ClineEventRunAborted, map[string]any{"reason": "aborted"})
	r.hub.reply(requestID, fakeReply{})
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionCreate)) == 2 }, "the approved mode applies")
	waitFor(t, func() bool { return !r.turnActive() }, "the settle releases the queue")
	assert.Len(t, r.hub.commandsNamed(commandSessionSendInput), 1, "no continuation after an interrupted turn")
	assert.Equal(t, contracts.ClinePermissionModeAct, r.agent.settings.permissionMode)
}

func TestARebuildThatFailsKeepsTheSession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.store(r.sessionID(), []any{})
	calls := 0
	r.hub.handle(commandSessionCreate, func(command fakeCommand) fakeReply {
		calls++
		if calls == 1 {
			return fakeReply{Code: "model_unavailable", Message: "no"}
		}
		var config map[string]any
		command.field("sessionConfig", &config)
		return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": config["sessionId"]}}}
	})
	err := r.agent.rebuildSession(clineSettings{model: testModel, effort: agent.EffortAuto, permissionMode: contracts.ClinePermissionModePlan})
	require.Error(t, err)
	assert.Equal(t, 2, calls, "the old settings build the session again")
	assert.Equal(t, contracts.ClinePermissionModeAct, r.agent.settings.permissionMode)
}

func TestARunEndDuringARebuildEndsNoTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Mu.Lock()
	r.agent.turn = turnState{active: true, settling: true, startedAt: time.Now()}
	r.agent.Mu.Unlock()
	r.feed(t, contracts.ClineEventRunAborted, map[string]any{"reason": "aborted"})
	assert.True(t, r.turnActive(), "the detach's run end belongs to no turn")
	assert.Zero(t, r.sink.MessageCount())
}

// A plan approval with Clear Context never reaches Cline: the service keeps the
// answer, clears the context while the plan's turn waits on the approval, moves
// the fresh session to the approved mode, and sends the plan as its first
// message. The agent aborts the waiting run, withdraws the approval, opens the
// fresh session in Plan mode, builds it again in Act mode with no hook and no
// plugin, and runs the plan there. The old session goes on with nothing.
func TestAPlanApprovedWithClearContextRunsInAFreshSession(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	oldSessionID := r.sessionID()
	r.startTurn(t, "Plan it.")
	r.emit(contracts.ClineEventAssistantFinished, map[string]any{"text": "Plan: edit it."})
	r.emit(contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the plan approval is published")

	freshSessionID, err := r.agent.ClearContext()
	require.NoError(t, err)
	require.NotEqual(t, oldSessionID, freshSessionID)
	aborts := r.hub.commandsNamed(commandRunAbort)
	require.Len(t, aborts, 1, "the clear aborts the run that waits on the approval")
	assert.Equal(t, oldSessionID, aborts[0].SessionID)
	assert.Contains(t, r.sink.CanceledControls(), "p1", "the approval is withdrawn")
	assert.Empty(t, approvalReplies(t, r.hub), "Cline never sees the approval")
	assert.False(t, r.turnActive())
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2)
	var fresh map[string]any
	require.True(t, creates[1].field("runtimeOptions", &fresh))
	assert.Equal(t, sessionModePlan, fresh["mode"], "the fresh session opens in the agent's mode")

	r.hub.store(freshSessionID, []any{})
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAct))
	creates = r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 3, "the approved mode builds the fresh session again")
	assert.Equal(t, freshSessionID, creates[2].SessionID)
	var built map[string]any
	require.True(t, creates[2].field("runtimeOptions", &built))
	assert.Equal(t, sessionModeAct, built["mode"])
	extensions, present := configExtensionsOf(t, creates[2])
	assert.True(t, present)
	assert.Equal(t, []string{"rules", "skills", "workflows"}, extensions)
	assert.False(t, r.turnActive(), "the rebuild releases the input queue")

	require.NoError(t, r.agent.SendInput("Execute the plan.", nil))
	sends := r.hub.commandsNamed(commandSessionSendInput)
	require.Len(t, sends, 2)
	assert.Equal(t, freshSessionID, sends[1].SessionID)
	assert.Equal(t, sessionModeAct, sends[1].str("mode"))
	for _, send := range sends {
		assert.NotEqual(t, actModeContinuationPrompt, send.str("prompt"), "no plan continuation reaches either session")
	}
}

// A resume fails and keeps no claim when Cline cannot state who holds the
// session, cannot read its conversation, or cannot create its runtime.
func TestAFailedResumeReleasesItsClaim(t *testing.T) {
	t.Parallel()
	refuse := func(fakeCommand) fakeReply { return fakeReply{Code: "internal_error", Message: "the store is locked"} }
	for _, tc := range []struct {
		name    string
		command string
		want    string
	}{
		{"the holder cannot be read", commandSessionGet, "read the Cline session"},
		{"the conversation cannot be read", commandSessionMessages, "read the Cline session"},
		{"the runtime cannot be created", commandSessionCreate, "create the Cline session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.noSession = true })
			sessionID := "resume-fail-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
			r.hub.store(sessionID, []any{map[string]any{"role": "user", "content": "Hi."}})
			r.hub.handle(tc.command, refuse)
			_, err := r.agent.openSession(context.Background(), r.agent.settings, sessionID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "not in Cline's session store", "only a missing session reads as missing")
			assert.Empty(t, r.sessionID(), "the agent holds no session")
			require.NoError(t, claimSession(sessionID, "other-agent"), "the failed resume released its claim")
			releaseSession(sessionID, "other-agent")
			assert.Empty(t, r.agent.claims)
		})
	}
}

// Cline can create a resumed session under another id. The agent then drives
// that id, and claims it in place of the one it asked for.
func TestAResumeUnderAnotherIDClaimsTheNewID(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	stored := "asked-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
	given := "given-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
	r.hub.store(stored, []any{})
	r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
		return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": given}}}
	})
	opened, err := r.agent.openSession(context.Background(), r.agent.settings, stored)
	require.NoError(t, err)
	t.Cleanup(r.agent.releaseClaims)
	assert.Equal(t, given, opened)
	assert.Equal(t, given, r.sessionID())
	r.waitSubscribed(t, given)
	require.NoError(t, claimSession(stored, "other-agent"), "the id that the agent asked for is free")
	releaseSession(stored, "other-agent")
	require.ErrorIs(t, claimSession(given, "other-agent"), errSessionHosted, "the id that the agent drives is claimed")
}

// A reply that states no session id opens no session.
func TestACreateThatStatesNoSessionFails(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
		return fakeReply{Payload: map[string]any{"session": map[string]any{}}}
	})
	_, err := r.agent.openSession(context.Background(), r.agent.settings, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the reply states no session id")
	assert.Empty(t, r.sessionID())
	assert.Empty(t, r.hub.subscribeFrames())
}

// A context clear that fails keeps the session that the agent drives, with its
// claim and its stream, and keeps no claim of the new session.
func TestAFailedContextClearKeepsTheOldSession(t *testing.T) {
	t.Parallel()
	t.Run("the new session cannot be created", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		old := r.sessionID()
		r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply { return fakeReply{Code: "internal_error", Message: "no"} })
		_, err := r.agent.ClearContext()
		require.Error(t, err)
		assert.Equal(t, old, r.sessionID())
		assert.Empty(t, r.sink.LastSessionID())
		require.ErrorIs(t, claimSession(old, "other-agent"), errSessionHosted, "the agent keeps its claim")
	})
	t.Run("another agent runs the new session", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		old := r.sessionID()
		taken := "taken-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
		require.NoError(t, claimSession(taken, "other-agent"))
		t.Cleanup(func() { releaseSession(taken, "other-agent") })
		r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
			return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": taken}}}
		})
		_, err := r.agent.ClearContext()
		require.ErrorIs(t, err, errSessionHosted)
		assert.Equal(t, old, r.sessionID())
		detaches := r.hub.commandsNamed(commandSessionDetach)
		require.Len(t, detaches, 1, "the agent leaves the session that it cannot drive")
		assert.Equal(t, taken, detaches[0].str("sessionId"))
		// The hub reads the frames of one connection in order, so an unsubscribe
		// that the clear wrote reaches it before this command.
		_, err = r.agent.hub.command(context.Background(), "probe", "", nil)
		require.NoError(t, err)
		assert.Empty(t, r.hub.unsubscribeFrames(), "the stream stays on the old session")
		assert.Len(t, r.hub.subscribeFrames(), 1, "no subscription of the new session")
	})
	t.Run("the new session cannot be subscribed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		old := r.sessionID()
		fresh := "fresh-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
		r.hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
			return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": fresh}}}
		})
		r.agent.subscribe = func(context.Context, string) error { return errHubConnectionLost }
		_, err := r.agent.ClearContext()
		require.ErrorIs(t, err, errHubConnectionLost)
		assert.Equal(t, old, r.sessionID())
		require.NoError(t, claimSession(fresh, "other-agent"), "the new session's claim is released")
		releaseSession(fresh, "other-agent")
		require.ErrorIs(t, claimSession(old, "other-agent"), errSessionHosted, "the agent keeps the old session's claim")
		assert.Empty(t, r.hub.commandsNamed(commandSessionDetach), "the old session stays attached")
	})
}

// A context clear ends the teammate runs of the old session, and the new
// session starts with no team: output with no turn then opens a turn again.
func TestClearContextEndsTheTeammateRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(r.sessionID(), contracts.ClineTeamRunEventRunStarted, "run_1", "researcher"))
	_, err := r.agent.ClearContext()
	require.NoError(t, err)
	item, ok := r.sink.BackgroundTask(teamRowPrefix + "run_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, item.Status)
	r.feed(t, eventAssistantDelta, map[string]any{"text": "lead"})
	assert.True(t, r.turnActive(), "no teammate run holds the new session's output")
}

// After an approved plan, the session builds again in Act mode, and a settling
// turn holds the input queue meanwhile. An interrupt in that window stops the
// continuation: the user stopped the agent, so the model does not go on with
// the plan. The approved mode still applies, as it does for a plan whose turn
// the user interrupted (TestAPlanApprovedAndThenInterruptedSwitchesWithoutGoingOn).
// A settling turn has no run, so the interrupt aborts nothing.
func TestAnInterruptDuringThePlanSwitchStopsTheContinuation(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	r.hub.store(r.sessionID(), []any{})
	detached := make(chan string, 1)
	r.hub.handle(commandSessionDetach, func(c fakeCommand) fakeReply {
		detached <- c.RequestID
		return fakeReply{Hold: true}
	})
	requestID := r.startTurn(t, "Go ahead.")
	r.emit(contracts.ClineEventAssistantFinished, map[string]any{"text": "Plan: edit it."})
	r.emit(contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the plan approval is published")
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "p1", Approved: true})))
	tool := question(r, "tool-1")
	tool["capabilityName"] = capabilitySwitchToActMode
	tool["payload"] = map[string]any{"toolName": contracts.ClineToolSwitchToActMode, "input": map[string]any{}}
	r.emit(contracts.ClineEventCapabilityRequested, tool)
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandCapabilityRespond)) == 1 }, "the plan tool runs")
	r.emit(contracts.ClineEventRunCompleted, map[string]any{"reason": "completed"})
	r.hub.reply(requestID, fakeReply{})

	detachID := <-detached
	require.True(t, r.turnActive(), "the settling turn holds the input queue")
	require.NoError(t, r.agent.Interrupt())
	assert.Empty(t, r.hub.commandsNamed(commandRunAbort), "a settling turn has no run to abort")
	r.hub.reply(detachID, fakeReply{})

	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionCreate)) == 2 }, "the approved mode applies")
	waitFor(t, func() bool { return !r.turnActive() }, "the settle releases the input queue")
	assert.Len(t, r.hub.commandsNamed(commandSessionSendInput), 1, "no continuation after the interrupt")
	r.agent.Mu.Lock()
	mode := r.agent.settings.permissionMode
	r.agent.Mu.Unlock()
	assert.Equal(t, contracts.ClinePermissionModeAct, mode)
}
