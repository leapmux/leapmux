package kimi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func approvalEvent(approvalID string, display map[string]any) map[string]any {
	return map[string]any{"type": contracts.KimiEventApprovalRequested, "approval_id": approvalID, "agent_id": kimiMainAgentID,
		"tool_name": contracts.KimiToolBash, "tool_input_display": display}
}

func questionEvent(questionID string) map[string]any {
	return map[string]any{"type": contracts.KimiEventQuestionRequested, "question_id": questionID, "agent_id": kimiMainAgentID,
		"questions": []map[string]any{{"id": "q_0", "question": "Color?", "options": []map[string]any{{"id": "opt_0_0", "label": "Red"}}}}}
}

// answerFor resolves a browser answer against the request the sink stored, as
// the service does, and returns what SendRawInput receives.
func answerFor(t *testing.T, rig *kimiTestRig, requestID, behavior string, fields map[string]any, plan *leapmuxv1.PlanApprovalSettings) []byte {
	t.Helper()
	var stored []byte
	for _, control := range rig.sink.PublishedControls() {
		if control.RequestID == requestID {
			stored = control.Payload
		}
	}
	require.NotNil(t, stored, "request %q was published", requestID)
	res := resolve(t, requestID, string(stored), browserAnswer(t, requestID, behavior, fields), plan)
	require.False(t, res.Withhold)
	return res.Content
}

func TestKimiPublishesControlRequests(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "turnId": 0, "delta": "I will run ls."})
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	rig.feed(t, questionEvent("question_1"))

	controls := rig.sink.PublishedControls()
	require.Len(t, controls, 2, "a repeated request publishes once")
	assert.Equal(t, "approval_1", controls[0].RequestID)
	assert.Equal(t, "question_1", controls[1].RequestID)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(controls[0].Payload, &payload))
	assert.Equal(t, contracts.KimiEventApprovalRequested, payload["type"], "the request is the server's own payload")

	messages := rig.sink.Messages()
	require.NotEmpty(t, messages)
	_, text := assembledRow(t, messages[0])
	assert.Equal(t, "I will run ls.", text, "the text before the request is flushed above its banner")
}

func TestKimiPlanReviewKeepsLeapMuxsCopyOfThePlan(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# Refactor the parser\n\n1. Split it."}))
	require.Equal(t, 1, rig.sink.PlanUpdateCount())
	update := rig.sink.LastPlanUpdate()
	assert.Equal(t, "# Refactor the parser\n\n1. Split it.", string(update.Content))
	assert.Equal(t, "Refactor the parser", update.Title)

	sub := approvalEvent("approval_2", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# A subagent's plan"})
	sub["agent_id"] = "agent-0"
	rig.feed(t, sub)
	assert.Equal(t, 1, rig.sink.PlanUpdateCount(), "a subagent's plan is not the session's plan")

	rig.feed(t, approvalEvent("approval_3", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "   "}))
	assert.Equal(t, 1, rig.sink.PlanUpdateCount(), "an empty plan replaces nothing")
}

func TestKimiDeliversAnswers(t *testing.T) {
	t.Parallel()

	t.Run("an approval", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", map[string]any{"scope": "session"}, nil)))
		posts := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""))
		require.Len(t, posts, 1)
		assert.JSONEq(t, `{"decision":"approved","scope":"session"}`, string(posts[0].Body))

		// The server's resolution that follows an answer withdraws nothing.
		rig.feed(t, map[string]any{"type": contracts.KimiEventApprovalResolved, "approval_id": "approval_1", "decision": "approved"})
		assert.Empty(t, rig.sink.CanceledControls())
		require.ErrorIs(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil)), errKimiControlGone,
			"a resolved request takes no second answer")
	})

	t.Run("a plan approval switches the mode before it leaves plan mode", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDPermissionMode, contracts.KimiModePlan)})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# Plan"}))
		content := answerFor(t, rig, "approval_1", "allow", nil, &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeAuto})
		require.NoError(t, rig.agent.SendRawInput(content))

		profile := kimiSessionPath(rig.sessionID(), "/profile")
		approval := kimiItemPath(rig.sessionID(), "approvals", "approval_1", "")
		routes := rig.fake.routes()
		profileAt, approvalAt := -1, -1
		for i, route := range routes {
			switch route {
			case "POST " + profile:
				profileAt = i
			case "POST " + approval:
				approvalAt = i
			}
		}
		require.GreaterOrEqual(t, approvalAt, 0)
		assert.Less(t, profileAt, approvalAt, "the mode is applied while plan mode still holds")
		profiles := rig.fake.requestsTo("POST " + profile)
		assert.JSONEq(t, `{"agent_config":{"permission_mode":"auto"}}`, string(profiles[len(profiles)-1].Body))
		posts := rig.fake.requestsTo("POST " + approval)
		assert.JSONEq(t, `{"decision":"approved"}`, string(posts[0].Body), "the mode is LeapMux's own and never reaches the approval")
		session, _ := rig.fake.session(rig.sessionID())
		assert.Equal(t, contracts.KimiModeAuto, session.Permission)
		assert.Empty(t, rig.sink.PermissionModes(),
			"the axis reads Plan until the status update that leaves plan mode reports the new mode")
	})

	t.Run("a subagent's plan approval leaves the main agent's mode alone", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDPermissionMode, contracts.KimiModeYolo)})
		sub := approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# A subagent's plan"})
		sub["agentId"], sub["agent_id"] = "agent-0", "agent-0"
		rig.feed(t, sub)
		profile := "POST " + kimiSessionPath(rig.sessionID(), "/profile")
		before := len(rig.fake.requestsTo(profile))

		content := answerFor(t, rig, "approval_1", "allow", nil, &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeManual})
		require.NoError(t, rig.agent.SendRawInput(content))
		assert.Len(t, rig.fake.requestsTo(profile), before, "no profile write reaches the main session")
		posts := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""))
		require.Len(t, posts, 1)
		assert.JSONEq(t, `{"decision":"approved"}`, string(posts[0].Body))
		session, _ := rig.fake.session(rig.sessionID())
		assert.Equal(t, contracts.KimiModeYolo, session.Permission, "the main agent keeps Ask When Needed")
		assert.Empty(t, rig.sink.PermissionModes())
	})

	// The server switches the mode by itself when it takes a goal start with a
	// mode, and reports the switch on no event. The worker applies the mode
	// through the profile first, so LeapMux's copy follows the server's.
	t.Run("a goal start in a chosen mode moves the permission mode with it", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}))
		content := answerFor(t, rig, "approval_1", "allow", map[string]any{"choice": contracts.KimiGoalModeYolo}, nil)
		require.NoError(t, rig.agent.SendRawInput(content))

		profiles := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile"))
		require.NotEmpty(t, profiles)
		assert.JSONEq(t, `{"agent_config":{"permission_mode":"yolo"}}`, string(profiles[len(profiles)-1].Body))
		posts := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""))
		require.Len(t, posts, 1)
		assert.JSONEq(t, `{"decision":"approved","selected_label":"yolo"}`, string(posts[0].Body),
			"the label still rides, so the server's own reading of it agrees")
		assert.Equal(t, []string{contracts.KimiModeYolo}, rig.sink.PermissionModes())
		assert.Equal(t, contracts.KimiModeYolo, agent.CurrentOptions(rig.agent.OptionGroups())[agent.OptionIDPermissionMode])
	})

	t.Run("a goal start in the current mode writes no mode", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}))
		before := len(rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile")))
		require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil)))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/profile")), before)
		assert.Empty(t, rig.sink.PermissionModes())
	})

	t.Run("a question's answers", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, questionEvent("question_1"))
		content := answerFor(t, rig, "question_1", "allow", map[string]any{"answers": map[string]any{"q_0": map[string]any{"kind": "single", "option_id": "opt_0_0"}}}, nil)
		require.NoError(t, rig.agent.SendRawInput(content))
		posts := rig.fake.requestsTo("POST " + kimiItemPath(rig.sessionID(), "questions", "question_1", ""))
		require.Len(t, posts, 1)
		assert.JSONEq(t, `{"answers":{"q_0":{"kind":"single","option_id":"opt_0_0"}},"method":"click"}`, string(posts[0].Body))
	})

	t.Run("a dismissal succeeds with the server's own non-zero code", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, questionEvent("question_1"))
		route := kimiItemPath(rig.sessionID(), "questions", "question_1", kimiActionDismiss)
		rig.fake.reply("POST "+route, fakeKapReply{Code: kimiCodeQuestionDismissed, Msg: "dismissed"})
		require.NoError(t, rig.agent.SendRawInput(answerFor(t, rig, "question_1", "deny", nil, nil)))
		assert.Len(t, rig.fake.requestsTo("POST "+route), 1)
	})

	t.Run("an answer another path already gave", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""), fakeKapReply{Code: kimiCodeAlreadyResolved})
		require.ErrorIs(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil)), errKimiControlGone)
	})

	t.Run("an answer whose reply was lost", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""), fakeKapReply{Drop: true})
		require.ErrorIs(t, rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil)), agent.ErrDeliveryUncertain)
	})

	t.Run("a refusal the server states", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
		rig.fake.reply("POST "+kimiItemPath(rig.sessionID(), "approvals", "approval_1", ""), fakeKapReply{Code: 40001, Msg: "bad decision"})
		err := rig.agent.SendRawInput(answerFor(t, rig, "approval_1", "allow", nil, nil))
		require.Error(t, err)
		assert.False(t, errors.Is(err, errKimiControlGone) || errors.Is(err, agent.ErrDeliveryUncertain))
	})
}

func TestKimiRefusesAnswersItCannotDeliver(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})

	for name, raw := range map[string]string{
		"no request id":   `{"response":{"request_id":"","response":{"decision":"approved"}}}`,
		"no answer":       `{"response":{"request_id":"approval_1"}}`,
		"an unsafe id":    `{"response":{"request_id":"../x","response":{"decision":"approved"}}}`,
		"an unknown id":   `{"response":{"request_id":"approval_404","response":{"decision":"approved"}}}`,
		"not an envelope": `[1,2]`,
	} {
		assert.Error(t, rig.agent.SendRawInput([]byte(raw)), name)
	}

	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	rig.agent.Mu.Lock()
	rig.agent.controls["approval_1"].sessionID = "session_old"
	rig.agent.Mu.Unlock()
	err := rig.agent.SendRawInput([]byte(`{"response":{"request_id":"approval_1","response":{"decision":"approved"}}}`))
	require.ErrorContains(t, err, "previous session")
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath(rig.sessionID(), "approvals", "approval_1", "")))
}

func TestKimiWithdrawsWhatTheServerResolvedByItself(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}))
	rig.feed(t, questionEvent("question_1"))
	rig.feed(t, questionEvent("question_2"))

	// The turn was aborted: the server resolves both with no decision.
	rig.feed(t, map[string]any{"type": contracts.KimiEventApprovalResolved, "approval_id": "approval_1"})
	rig.feed(t, map[string]any{"type": contracts.KimiEventQuestionDismissed, "question_id": "question_1"})
	assert.Equal(t, []string{"approval_1", "question_1"}, rig.sink.CanceledControls())

	rig.feed(t, map[string]any{"type": contracts.KimiEventQuestionAnswered, "question_id": "question_unknown"})
	rig.feed(t, map[string]any{"type": contracts.KimiEventApprovalResolved})
	assert.Len(t, rig.sink.CanceledControls(), 2, "a resolution of a request LeapMux never published withdraws nothing")

	rig.agent.withdrawAllControls()
	assert.Equal(t, []string{"approval_1", "question_1", "question_2"}, rig.sink.CanceledControls())
}

func TestKimiCancelsARequestNoUserCanAnswer(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		event map[string]any
		route func(sessionID string) string
		body  string
	}{
		"an approval": {
			event: approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}),
			route: func(sessionID string) string { return kimiItemPath(sessionID, "approvals", "approval_1", "") },
			body:  `{"decision":"cancelled"}`,
		},
		"a question": {
			event: questionEvent("question_1"),
			route: func(sessionID string) string {
				return kimiItemPath(sessionID, "questions", "question_1", kimiActionDismiss)
			},
			body: `{}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.sink.PublicationError = errors.New("the store is full")
			rig.feed(t, tc.event)
			route := "POST " + tc.route(rig.sessionID())
			waitFor(t, func() bool { return len(rig.fake.requestsTo(route)) == 1 }, "the unpublished request is cancelled")
			assert.JSONEq(t, tc.body, string(rig.fake.requestsTo(route)[0].Body))
			rig.agent.Mu.Lock()
			pending := len(rig.agent.controls)
			rig.agent.Mu.Unlock()
			assert.Zero(t, pending)
		})
	}
}

func TestClassifyKimiControlError(t *testing.T) {
	t.Parallel()

	assert.NoError(t, classifyKimiControlError(nil))
	assert.ErrorIs(t, classifyKimiControlError(&kimiAPIError{Code: kimiCodeAlreadyResolved}), errKimiControlGone)
	other := &kimiAPIError{Code: 40001}
	assert.Same(t, error(other), classifyKimiControlError(other))
	assert.ErrorIs(t, classifyKimiControlError(errors.New("connection reset")), agent.ErrDeliveryUncertain)
}

func TestKimiIgnoresARequestWithNoID(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	approval := approvalEvent("", map[string]any{"kind": "command", "command": "ls"})
	rig.feed(t, approval)
	question := questionEvent("")
	rig.feed(t, question)
	assert.Zero(t, rig.sink.PublishedControlCount(), "no answer could address a request with no id")
	rig.agent.Mu.Lock()
	pending := len(rig.agent.controls)
	rig.agent.Mu.Unlock()
	assert.Zero(t, pending)
}

// The server fills the interaction's agent tag and the envelope's agent from
// the same tag. A request that states no tag belongs to the envelope's agent.
func TestKimiApprovalWithNoAgentTagTakesTheEnvelopeAgent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)

	sub := approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# A subagent's plan"})
	delete(sub, "agent_id")
	sub["agentId"] = "agent-0"
	rig.feed(t, sub)
	assert.Zero(t, rig.sink.PlanUpdateCount(), "the envelope states a subagent, whose plan is not the session's")
	assert.Equal(t, 1, rig.sink.PublishedControlCount(), "the user still answers it")

	main := approvalEvent("approval_2", map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# The session's plan"})
	delete(main, "agent_id")
	rig.feed(t, main)
	require.Equal(t, 1, rig.sink.PlanUpdateCount(), "the envelope states the main agent")
	assert.Equal(t, "The session's plan", rig.sink.LastPlanUpdate().Title)
}

// After a gap that the stream could not replay, the snapshot's pending
// interactions are the truth for the session that the gap cut.
func TestKimiReconcileControls(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineKimiAgent(t, sink)
	a.controls = map[string]*kimiPendingControl{
		"approval_gone":     {kind: kimiControlApproval, sessionID: "session_1"},
		"approval_answered": {kind: kimiControlApproval, sessionID: "session_1", answered: true},
		"approval_old":      {kind: kimiControlApproval, sessionID: "session_old"},
		"question_kept":     {kind: kimiControlQuestion, sessionID: "session_1"},
	}

	a.reconcileControls("session_1",
		[]json.RawMessage{json.RawMessage(`not json`), json.RawMessage(`{"approval_id":""}`), json.RawMessage(`null`)},
		[]json.RawMessage{json.RawMessage(`{"question_id":"question_kept","agent_id":"main","questions":[]}`)})

	assert.Equal(t, []string{"approval_gone"}, sink.CanceledControls(),
		"only the request that the server resolved and nobody answered leaves the banner")
	assert.Zero(t, sink.PublishedControlCount(), "a request that is published already is not published twice")
	a.Mu.Lock()
	remaining := make([]string, 0, len(a.controls))
	for id := range a.controls {
		remaining = append(remaining, id)
	}
	a.Mu.Unlock()
	assert.ElementsMatch(t, []string{"approval_old", "question_kept"}, remaining,
		"a request of another session is not this snapshot's to judge")
}

func TestKimiInteractionEvent(t *testing.T) {
	t.Parallel()

	t.Run("an approval reads as the event that announced it", func(t *testing.T) {
		t.Parallel()
		item := json.RawMessage(`{"approval_id":"approval_1","tool_name":"Bash","type":"stale","sessionId":"session_old"}`)
		event, requestID, ok := kimiInteractionEvent(contracts.KimiEventApprovalRequested, "session_1", item)
		require.True(t, ok)
		assert.Equal(t, "approval_1", requestID)
		assert.Equal(t, contracts.KimiEventApprovalRequested, event.Type)
		assert.Equal(t, kimiMainAgentID, event.AgentID, "an interaction with no agent tag is the main agent's")
		assert.JSONEq(t, `{"approval_id":"approval_1","tool_name":"Bash","type":"event.approval.requested",`+
			`"agentId":"main","sessionId":"session_1"}`, string(event.Raw), "the envelope fields replace what the item held")
	})

	t.Run("a question keeps its agent", func(t *testing.T) {
		t.Parallel()
		item := json.RawMessage(`{"question_id":"question_1","agent_id":"agent-2"}`)
		event, requestID, ok := kimiInteractionEvent(contracts.KimiEventQuestionRequested, "session_1", item)
		require.True(t, ok)
		assert.Equal(t, "question_1", requestID)
		assert.Equal(t, "agent-2", event.AgentID)
	})

	for name, tc := range map[string]struct {
		eventType string
		item      string
	}{
		"bytes that are not JSON":            {contracts.KimiEventApprovalRequested, `not json`},
		"a null item":                        {contracts.KimiEventApprovalRequested, `null`},
		"an array":                           {contracts.KimiEventApprovalRequested, `[]`},
		"an approval with no id":             {contracts.KimiEventApprovalRequested, `{"tool_name":"Bash"}`},
		"a question that states an approval": {contracts.KimiEventQuestionRequested, `{"approval_id":"approval_1"}`},
		"an id of the wrong type":            {contracts.KimiEventApprovalRequested, `{"approval_id":7}`},
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			t.Parallel()
			_, _, ok := kimiInteractionEvent(tc.eventType, "session_1", json.RawMessage(tc.item))
			assert.False(t, ok)
		})
	}
}

func TestKimiRefusesAnAnswerItCannotPost(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		requestID string
		event     map[string]any
		answer    string
		refusal   string
	}{
		"an approval that is not an object": {
			requestID: "approval_1",
			event:     approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"}),
			answer:    `{"response":{"request_id":"approval_1","response":"approved"}}`,
			refusal:   "decode the Kimi Code approval",
		},
		"a question answer that is not an object": {
			requestID: "question_1",
			event:     questionEvent("question_1"),
			answer:    `{"response":{"request_id":"question_1","response":[1]}}`,
			refusal:   "decode the Kimi Code question answer",
		},
		"a permission mode that Kimi Code does not offer": {
			requestID: "approval_1",
			event:     approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}),
			answer:    `{"response":{"request_id":"approval_1","response":{"decision":"approved","permission_mode":"plan"}}}`,
			refusal:   "not one Kimi Code offers",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.feed(t, tc.event)
			profiles := len(rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile")))
			require.ErrorContains(t, rig.agent.SendRawInput([]byte(tc.answer)), tc.refusal)
			for _, route := range rig.fake.routes() {
				assert.NotContains(t, route, tc.requestID, "nothing reaches the server")
			}
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/profile")), profiles)
		})
	}

	t.Run("a control of a kind this build does not know", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.agent.Mu.Lock()
		rig.agent.controls = map[string]*kimiPendingControl{"approval_1": {kind: kimiControlKind(99), sessionID: rig.agent.sessionID}}
		rig.agent.Mu.Unlock()
		err := rig.agent.SendRawInput([]byte(`{"response":{"request_id":"approval_1","response":{"decision":"approved"}}}`))
		require.ErrorContains(t, err, "unknown Kimi Code control kind 99")
	})
}

// The mode switch goes first, so a switch that fails posts no approval: the
// agent would otherwise run in a mode the user did not choose. The request stays
// pending, and a second answer can still deliver it.
func TestKimiApprovalWhoseModeSwitchFailsStaysPending(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.feed(t, approvalEvent("approval_1", map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}))
	profile := "POST " + kimiSessionPath(rig.sessionID(), "/profile")
	approval := "POST " + kimiItemPath(rig.sessionID(), "approvals", "approval_1", "")
	content := answerFor(t, rig, "approval_1", "allow", map[string]any{"choice": contracts.KimiGoalModeYolo}, nil)

	rig.fake.reply(profile, fakeKapReply{Code: 50000, Msg: "profile locked"})
	err := rig.agent.SendRawInput(content)
	require.ErrorContains(t, err, "switch the permission mode for the approval")
	assert.Contains(t, err.Error(), "profile locked")
	assert.Empty(t, rig.fake.requestsTo(approval))
	assert.Empty(t, rig.sink.PermissionModes(), "the axis did not move")

	rig.fake.reply(profile, fakeKapReply{})
	require.NoError(t, rig.agent.SendRawInput(content))
	assert.Len(t, rig.fake.requestsTo(approval), 1)
	assert.Equal(t, []string{contracts.KimiModeYolo}, rig.sink.PermissionModes())
}

func TestKimiCancelUnpublishedControlRefusesAnUnsafeID(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	before := len(rig.fake.routes())
	rig.agent.cancelUnpublishedControl(rig.sessionID(), "../approvals", kimiControlApproval)
	rig.agent.cancelUnpublishedControl("../session", "question_1", kimiControlQuestion)
	assert.Len(t, rig.fake.routes(), before, "an id that Kimi Code does not issue could reach another route")
}
