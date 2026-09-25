package cline

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// approval is the payload of one approval.requested.
func approval(id, tool string) map[string]any {
	return map[string]any{
		"approvalId": id, "sessionId": "", "agentId": "agent_1", "toolCallId": "call_" + id,
		"toolName": tool, "inputJson": `{"commands":["ls"]}`,
	}
}

// question is the payload of one ask_question capability request.
func question(r *rig, id string) map[string]any {
	return map[string]any{
		"requestId": id, "sessionId": r.sessionID(), "targetClientId": r.agent.clientID,
		"capabilityName": contracts.ClineCapabilityAskQuestion,
		"payload": map[string]any{
			"executor": executorAskQuestion,
			"args":     []any{"Which color?", []string{"Red", "Blue"}},
			"context":  map[string]any{"toolCallId": "call_q", "agentId": "agent_1"},
		},
	}
}

// capabilityReplies returns every capability.respond payload the hub received.
func capabilityReplies(t *testing.T, hub *fakeHub) []contracts.ClineCapabilityReply {
	t.Helper()
	var out []contracts.ClineCapabilityReply
	for _, command := range hub.commandsNamed(commandCapabilityRespond) {
		var reply contracts.ClineCapabilityReply
		require.NoError(t, json.Unmarshal(command.Payload, &reply))
		out = append(out, reply)
	}
	return out
}

// approvalReplies returns every approval.respond payload the hub received.
func approvalReplies(t *testing.T, hub *fakeHub) []contracts.ClineApprovalReply {
	t.Helper()
	var out []contracts.ClineApprovalReply
	for _, command := range hub.commandsNamed(commandApprovalRespond) {
		var reply contracts.ClineApprovalReply
		require.NoError(t, json.Unmarshal(command.Payload, &reply))
		out = append(out, reply)
	}
	return out
}

func TestActAsksBeforeATool(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", contracts.ClineToolRunCommands))
	require.Equal(t, 1, r.sink.PublishedControlCount())
	control := r.sink.LastPublishedControl()
	assert.Equal(t, "a1", control.RequestID)
	assert.Equal(t, contracts.ClineEventApprovalRequested, decode(t, control.Payload)["event"], "the request is Cline's own event")
	assert.Empty(t, approvalReplies(t, r.hub), "nothing answers until the user does")
}

func TestActRunsTheToolsThatOnlyRead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, tool := range []string{"read_files", "search_codebase", "skills", "ask_followup_question", contracts.ClineToolAskQuestion, "submit_and_exit"} {
		r.feed(t, contracts.ClineEventApprovalRequested, approval("safe-"+tool, tool))
	}
	assert.Zero(t, r.sink.PublishedControlCount())
	waitFor(t, func() bool { return len(approvalReplies(t, r.hub)) == 6 }, "every safe tool is approved")
	for _, reply := range approvalReplies(t, r.hub) {
		assert.True(t, reply.Approved)
		assert.Empty(t, reply.PermissionMode, "the reply carries no LeapMux field")
	}
}

// A web fetch sends what the model chooses to a host that the model chooses. A
// read with no banner and a fetch with no banner together send any file to any
// page, so Act and Plan ask before a fetch.
func TestActAndPlanAskBeforeAWebFetch(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{contracts.ClinePermissionModeAct, contracts.ClinePermissionModePlan} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options(agent.OptionIDPermissionMode, mode) })
			r.feed(t, contracts.ClineEventApprovalRequested, approval("fetch-1", "fetch_web_content"))
			assert.Equal(t, 1, r.sink.PublishedControlCount())
			assert.Empty(t, approvalReplies(t, r.hub))
		})
	}
}

func TestAutoApproveRunsEveryTool(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a2", contracts.ClineToolSpawnAgent))
	assert.Zero(t, r.sink.PublishedControlCount())
	waitFor(t, func() bool { return len(approvalReplies(t, r.hub)) == 2 }, "both calls are approved")
}

func TestPlanAsksBeforeACommand(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", contracts.ClineToolRunCommands))
	assert.Equal(t, 1, r.sink.PublishedControlCount())
}

func TestARepeatedApprovalIsPublishedOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	assert.Equal(t, 1, r.sink.PublishedControlCount())
}

// The hub states each pending question again to a client that subscribes again
// after a reconnect, so a repeat raises no second card.
func TestARepeatedQuestionIsPublishedOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	assert.Equal(t, 1, r.sink.PublishedControlCount())
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))
}

// A question that no user can see must not wait for an answer that cannot come.
func TestAQuestionNoUserCanSeeIsRefused(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{PublicationError: errors.New("the database is gone")}
	r := newRig(t, func(c *rigConfig) { c.sink = sink })
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	waitFor(t, func() bool { return len(capabilityReplies(t, r.hub)) == 1 }, "the question is answered")
	reply := capabilityReplies(t, r.hub)[0]
	assert.Equal(t, "q1", reply.RequestId)
	assert.False(t, reply.Ok)
	assert.Contains(t, reply.Error, "could not show the question")
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "q1", Ok: true})), errUnknownControl,
		"a refused question takes no later answer")
}

func TestAnApprovalNoUserCanSeeIsRefused(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{PublicationError: errors.New("the database is gone")}
	r := newRig(t, func(c *rigConfig) { c.sink = sink })
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	waitFor(t, func() bool { return len(approvalReplies(t, r.hub)) == 1 }, "the approval is answered")
	reply := approvalReplies(t, r.hub)[0]
	assert.False(t, reply.Approved, "fail closed")
	assert.Contains(t, reply.Reason, "could not show")
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), errUnknownControl,
		"a refused request takes no later answer")
}

func TestTheUsersApprovalReachesCline(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: false, Reason: "No."})))
	replies := approvalReplies(t, r.hub)
	require.Len(t, replies, 1)
	assert.Equal(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: false, Reason: "No."}, replies[0])
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), errUnknownControl, "an answered request takes no second answer")
}

func TestAnAnswerOfAnOldSessionIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	r.agent.Mu.Lock()
	r.agent.sessionID = "replaced"
	r.agent.Mu.Unlock()
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), agent.ErrInputSessionChanged)
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "q1", Ok: true})), agent.ErrInputSessionChanged)
	assert.Empty(t, approvalReplies(t, r.hub))
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))
}

func TestSendRawInputRefusesWhatIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	for _, tc := range []struct {
		data string
		want string
	}{
		{`not json`, "not JSON"},
		{`{"other":1}`, "neither an approval nor a question answer"},
		{`{"approvalId":5,"approved":true}`, "read the Cline approval answer"},
		{`{"requestId":["q1"],"ok":true}`, "read the Cline question answer"},
	} {
		err := r.agent.SendRawInput([]byte(tc.data))
		require.Error(t, err, tc.data)
		assert.Contains(t, err.Error(), tc.want, tc.data)
	}
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "q9", Ok: true})), errUnknownControl)
	assert.Empty(t, approvalReplies(t, r.hub), "no malformed answer reaches Cline")
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), "the requests still wait for their answers")
}

func TestSendRawInputRefusesAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.agent.Stop()
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), errAgentStopped)
	assert.Empty(t, approvalReplies(t, r.hub))
}

// An answer states its kind by its id field, and it answers only a request of
// that kind: an approval answer for a question is refused, and the question
// still takes its own answer.
func TestAnAnswerOfTheWrongKindIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "q1", Approved: true})), errUnknownControl)
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "a1", Ok: true})), errUnknownControl)
	assert.Empty(t, approvalReplies(t, r.hub))
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))

	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})))
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "q1", Error: "No."})))
	assert.Len(t, approvalReplies(t, r.hub), 1)
	assert.Equal(t, []contracts.ClineCapabilityReply{{RequestId: "q1", Error: "No."}}, capabilityReplies(t, r.hub))
}

// An answer that Cline refuses fails the call, so the user sees that it did not
// arrive. The request no longer waits: Cline states a request again that still
// waits after a reconnect, and that request takes a new answer.
func TestAnAnswerThatClineRefusesFails(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
		return fakeReply{Code: "approval_not_found", Message: "no such approval"}
	})
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	err := r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true}))
	var refused *HubCommandError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "approval_not_found", refused.Code)
	assert.Contains(t, err.Error(), "answer the Cline approval")
	require.ErrorIs(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "a1", Approved: true})), errUnknownControl)
}

// A request that states no id cannot take an answer, so the worker neither
// publishes it nor answers it.
func TestARequestWithNoIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	noApprovalID := approval("", "editor")
	r.feed(t, contracts.ClineEventApprovalRequested, noApprovalID)
	noRequestID := question(r, "")
	r.feed(t, contracts.ClineEventCapabilityRequested, noRequestID)
	r.agent.HandleOutput([]byte(`{"event":"approval.requested","sessionId":"` + r.sessionID() + `","payload":"not a request"}`))
	// A request with an id is answered, which shows that the mode answers. The
	// wait returns once every answer that the dispatcher started arrived.
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.agent.background.Wait()
	assert.Equal(t, []contracts.ClineApprovalReply{{ApprovalId: "a1", Approved: true}}, approvalReplies(t, r.hub))
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))
	assert.Zero(t, r.sink.PublishedControlCount())
}

func TestAQuestionIsPublishedAndAnswered(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventToolStarted, map[string]any{"toolCallId": "call_q", "toolName": contracts.ClineToolAskQuestion, "input": map[string]any{"question": "Which color?"}})
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	require.Equal(t, 1, r.sink.PublishedControlCount())
	control := r.sink.LastPublishedControl()
	assert.Equal(t, "q1", control.RequestID)
	assert.NotZero(t, control.SourceSeq, "the question links the call that asked it")

	payload, err := json.Marshal(map[string]string{contracts.ClineCapabilityReplyResult: "Blue"})
	require.NoError(t, err)
	require.NoError(t, r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "q1", Ok: true, Payload: payload})))
	commands := r.hub.commandsNamed(commandCapabilityRespond)
	require.Len(t, commands, 1)
	assert.JSONEq(t, `{"requestId":"q1","ok":true,"payload":{"result":"Blue"}}`, string(commands[0].Payload))
}

func TestAQuestionForAnotherClientIsLeftAlone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	request := question(r, "q1")
	request["targetClientId"] = "someone-else"
	r.feed(t, contracts.ClineEventCapabilityRequested, request)
	assert.Zero(t, r.sink.PublishedControlCount())
	assert.Empty(t, r.hub.commandsNamed(commandCapabilityRespond))
}

func TestAnUnknownCapabilityIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	request := question(r, "c1")
	request["capabilityName"] = "tool_executor.bash"
	r.feed(t, contracts.ClineEventCapabilityRequested, request)
	command, ok := r.hub.waitCommand(commandCapabilityRespond)
	require.True(t, ok, "the request is answered")
	var reply contracts.ClineCapabilityReply
	require.NoError(t, json.Unmarshal(command.Payload, &reply))
	assert.False(t, reply.Ok)
	assert.Contains(t, reply.Error, "tool_executor.bash")
}

func TestAResolvedRequestIsWithdrawn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q1"))
	r.feed(t, eventApprovalResolved, map[string]any{"approvalId": "a1", "cancelled": true})
	r.feed(t, eventCapabilityResolved, map[string]any{"requestId": "q1", "cancelled": true})
	assert.ElementsMatch(t, []string{"a1", "q1"}, r.sink.CanceledControls())
	r.feed(t, eventApprovalResolved, map[string]any{"approvalId": "a1"})
	assert.Len(t, r.sink.CanceledControls(), 2, "a resolution of a request the agent no longer holds withdraws nothing")

	// A resolution states its request by the id field of its own kind: an
	// approval's resolution cannot withdraw a question.
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "q2"))
	r.feed(t, eventApprovalResolved, map[string]any{"requestId": "q2"})
	r.feed(t, eventApprovalResolved, map[string]any{})
	r.agent.HandleOutput([]byte(`{"event":"capability.resolved","sessionId":"` + r.sessionID() + `","payload":"not a resolution"}`))
	assert.Len(t, r.sink.CanceledControls(), 2, "a resolution that states no id of its kind withdraws nothing")
	r.feed(t, eventCapabilityResolved, map[string]any{"requestId": "q2"})
	assert.Equal(t, []string{"a1", "q1", "q2"}, sortedCopy(r.sink.CanceledControls()))
}

// sortedCopy returns the values in order, for a comparison that ignores the
// order in which they arrived.
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	slices.Sort(out)
	return out
}

func TestTheTurnsEndWithdrawsItsRequests(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.emit(contracts.ClineEventApprovalRequested, approval("a1", "editor"))
	waitFor(t, func() bool { return r.sink.PublishedControlCount() == 1 }, "the approval is published")
	r.endRun(t, requestID, contracts.ClineRunReasonAborted)
	assert.Equal(t, []string{"a1"}, r.sink.CanceledControls())
}

func TestARequestOfADetachedSessionIsDeclined(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.HandleOutput(eventEnvelope(t, "old-session", contracts.ClineEventApprovalRequested, approval("a1", "editor")))
	request := question(r, "q1")
	r.agent.HandleOutput(eventEnvelope(t, "old-session", contracts.ClineEventCapabilityRequested, request))
	assert.Zero(t, r.sink.PublishedControlCount())
	waitFor(t, func() bool {
		return len(approvalReplies(t, r.hub)) == 1 && len(r.hub.commandsNamed(commandCapabilityRespond)) == 1
	}, "both requests are declined")
	assert.Equal(t, contracts.ClineApprovalReply{ApprovalId: "a1", Reason: "The session ended."}, approvalReplies(t, r.hub)[0])
	assert.Equal(t, contracts.ClineCapabilityReply{RequestId: "q1", Error: "The session ended."}, capabilityReplies(t, r.hub)[0])
}

func TestThePlanToolPublishesThePlan(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	r.feedTurn(t)
	r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "# Add the flag\n1. Edit main.go."})
	r.feed(t, contracts.ClineEventApprovalRequested, approval("p1", contracts.ClineToolSwitchToActMode))
	require.Equal(t, 1, r.sink.PlanUpdateCount())
	plan := r.sink.LastPlanUpdate()
	assert.Equal(t, "# Add the flag\n1. Edit main.go.", string(plan.Content))
	assert.Equal(t, "Add the flag", plan.Title)
	assert.Equal(t, 1, r.sink.PublishedControlCount())
}

func TestThePlanToolRunsOnlyInPlanMode(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	request := question(r, "t1")
	request["capabilityName"] = capabilitySwitchToActMode
	request["payload"] = map[string]any{"toolName": contracts.ClineToolSwitchToActMode, "input": map[string]any{}}
	r.feed(t, contracts.ClineEventCapabilityRequested, request)
	command, ok := r.hub.waitCommand(commandCapabilityRespond)
	require.True(t, ok, "the plan tool is answered")
	var reply contracts.ClineCapabilityReply
	require.NoError(t, json.Unmarshal(command.Payload, &reply))
	assert.False(t, reply.Ok)
	assert.Equal(t, "Already in act mode.", reply.Error)
}

// storedApproval is the stored request of an approval.
func storedApproval(t *testing.T, id, tool string) []byte {
	t.Helper()
	return eventEnvelope(t, "sess-1", contracts.ClineEventApprovalRequested, approval(id, tool))
}

// browserDecision is the browser's neutral decision envelope.
func browserDecision(t *testing.T, requestID, behavior, message string, extra map[string]any) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior, "message": message}
	for k, v := range extra {
		inner[k] = v
	}
	return mustJSON(t, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": inner}})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestResolveControlResponseApproves(t *testing.T) {
	t.Parallel()
	res := resolveControlResponse(agent.ControlResponseContext{
		RequestID: "a1", RequestPayload: storedApproval(t, "a1", "editor"),
		ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorAllow, "", nil),
	})
	require.False(t, res.Withhold)
	assert.JSONEq(t, `{"approvalId":"a1","approved":true}`, string(res.Content))
	assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponseDeclinesWithTheUsersReason(t *testing.T) {
	t.Parallel()
	res := resolveControlResponse(agent.ControlResponseContext{
		RequestID: "a1", RequestPayload: storedApproval(t, "a1", "editor"),
		ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorDeny, "Use the other file.", nil),
	})
	assert.JSONEq(t, `{"approvalId":"a1","approved":false,"reason":"Use the other file."}`, string(res.Content))

	res = resolveControlResponse(agent.ControlResponseContext{
		RequestID: "a1", RequestPayload: storedApproval(t, "a1", "editor"),
		ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorDeny, agent.ControlRejectedByUserMessage, nil),
	})
	assert.JSONEq(t, `{"approvalId":"a1","approved":false,"reason":"`+declinedToolReason+`"}`, string(res.Content))
}

func TestResolveControlResponseCarriesThePlansMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		picked string
		clear  bool
		want   string
	}{
		{"no pick", "", false, contracts.ClinePermissionModeAct},
		{"auto-approve", contracts.ClinePermissionModeAutoApprove, false, contracts.ClinePermissionModeAutoApprove},
		{"plan is not an exit", contracts.ClinePermissionModePlan, false, contracts.ClinePermissionModeAct},
		{"a context clear applies the mode itself", contracts.ClinePermissionModeAutoApprove, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := resolveControlResponse(agent.ControlResponseContext{
				RequestID: "p1", RequestPayload: storedApproval(t, "p1", contracts.ClineToolSwitchToActMode),
				ResponseContent: browserDecision(t, "p1", agent.ControlBehaviorAllow, "", nil),
				PlanApproval:    &leapmuxv1.PlanApprovalSettings{PermissionMode: tc.picked, ClearContext: tc.clear},
			})
			require.False(t, res.Withhold)
			assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
			var reply contracts.ClineApprovalReply
			require.NoError(t, json.Unmarshal(res.Content, &reply))
			assert.True(t, reply.Approved)
			assert.Equal(t, tc.want, reply.PermissionMode)
		})
	}
}

func TestResolveControlResponseRejectsAPlanWithoutAMode(t *testing.T) {
	t.Parallel()
	res := resolveControlResponse(agent.ControlResponseContext{
		RequestID: "p1", RequestPayload: storedApproval(t, "p1", contracts.ClineToolSwitchToActMode),
		ResponseContent: browserDecision(t, "p1", agent.ControlBehaviorDeny, "Split it.", nil),
	})
	var reply contracts.ClineApprovalReply
	require.NoError(t, json.Unmarshal(res.Content, &reply))
	assert.False(t, reply.Approved)
	assert.Empty(t, reply.PermissionMode)
	assert.Equal(t, "Split it.", reply.Reason)
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
}

func TestResolveControlResponseAnswersAQuestion(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	stored := eventEnvelope(t, "sess-1", contracts.ClineEventCapabilityRequested, question(r, "q1"))
	res := resolveControlResponse(agent.ControlResponseContext{
		RequestID: "q1", RequestPayload: stored,
		ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorAllow, "", map[string]any{contracts.ClineQuestionAnswerAnswer: "Blue"}),
	})
	require.False(t, res.Withhold)
	assert.JSONEq(t, `{"requestId":"q1","ok":true,"payload":{"result":"Blue"}}`, string(res.Content))

	res = resolveControlResponse(agent.ControlResponseContext{
		RequestID: "q1", RequestPayload: stored,
		ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorAllow, "", map[string]any{contracts.ClineQuestionAnswerAnswer: "  "}),
	})
	assert.True(t, res.Withhold, "an empty answer is not an answer")

	res = resolveControlResponse(agent.ControlResponseContext{
		RequestID: "q1", RequestPayload: stored,
		ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorDeny, "", nil),
	})
	assert.JSONEq(t, `{"requestId":"q1","ok":false,"error":"`+declinedQuestionError+`"}`, string(res.Content))

	res = resolveControlResponse(agent.ControlResponseContext{
		RequestID: "q1", RequestPayload: stored,
		ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorDeny, "Ask me later.", nil),
	})
	assert.JSONEq(t, `{"requestId":"q1","ok":false,"error":"Ask me later."}`, string(res.Content), "the user's own words decline the question")

	res = resolveControlResponse(agent.ControlResponseContext{
		RequestID: "q1", RequestPayload: stored,
		ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorAllow, "", nil),
	})
	assert.True(t, res.Withhold, "an answer that states no answer field is not an answer")
}

func TestResolveControlResponseWithholdsWhatItCannotRead(t *testing.T) {
	t.Parallel()
	good := storedApproval(t, "a1", "editor")
	for _, tc := range []struct {
		name string
		ctx  agent.ControlResponseContext
	}{
		{"no decision", agent.ControlResponseContext{RequestID: "a1", RequestPayload: good, ResponseContent: []byte(`{"response":{"request_id":"a1","response":{}}}`)}},
		{"another request", agent.ControlResponseContext{RequestID: "a1", RequestPayload: good, ResponseContent: browserDecision(t, "a2", agent.ControlBehaviorAllow, "", nil)}},
		{"a stored id that differs", agent.ControlResponseContext{RequestID: "a9", RequestPayload: good, ResponseContent: browserDecision(t, "a9", agent.ControlBehaviorAllow, "", nil)}},
		{"an event that is not a request", agent.ControlResponseContext{RequestID: "a1", RequestPayload: eventEnvelope(t, "s", contracts.ClineEventAssistantFinished, map[string]any{}), ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorAllow, "", nil)}},
		{"a capability that is not a question", agent.ControlResponseContext{RequestID: "c1", RequestPayload: eventEnvelope(t, "s", contracts.ClineEventCapabilityRequested, map[string]any{"requestId": "c1", "capabilityName": capabilitySwitchToActMode}), ResponseContent: browserDecision(t, "c1", agent.ControlBehaviorAllow, "", nil)}},
		{"an approval with no id", agent.ControlResponseContext{RequestID: "a1", RequestPayload: eventEnvelope(t, "s", contracts.ClineEventApprovalRequested, map[string]any{}), ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorAllow, "", nil)}},
		{"no stored request", agent.ControlResponseContext{RequestID: "a1", ResponseContent: browserDecision(t, "a1", agent.ControlBehaviorAllow, "", nil)}},
		{"a decision that is neither allow nor deny", agent.ControlResponseContext{RequestID: "a1", RequestPayload: good, ResponseContent: browserDecision(t, "a1", "ask", "", nil)}},
		{"a response that is not JSON", agent.ControlResponseContext{RequestID: "a1", RequestPayload: good, ResponseContent: []byte(`not json`)}},
		{"a question keyed by another id", agent.ControlResponseContext{RequestID: "q9", RequestPayload: eventEnvelope(t, "s", contracts.ClineEventCapabilityRequested, map[string]any{"requestId": "q1", "capabilityName": contracts.ClineCapabilityAskQuestion}), ResponseContent: browserDecision(t, "q9", agent.ControlBehaviorAllow, "", map[string]any{contracts.ClineQuestionAnswerAnswer: "Blue"})}},
		{"a question with no id", agent.ControlResponseContext{RequestID: "q1", RequestPayload: eventEnvelope(t, "s", contracts.ClineEventCapabilityRequested, map[string]any{"capabilityName": contracts.ClineCapabilityAskQuestion}), ResponseContent: browserDecision(t, "q1", agent.ControlBehaviorAllow, "", map[string]any{contracts.ClineQuestionAnswerAnswer: "Blue"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, resolveControlResponse(tc.ctx).Withhold)
		})
	}
}

func TestControlResponseConformance(t *testing.T) {
	t.Parallel()
	plugin := Registration().Plugin
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, plugin)
	agenttest.AssertPreservesTheResponseWithoutARequest(t, plugin)
}

// A request that states no session belongs to no session that the agent
// drives, so no mode answers it, Auto-approve included. The worker refuses it,
// so no run waits for it.
func TestARequestWithNoSessionIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	r.agent.HandleOutput(eventEnvelope(t, "", contracts.ClineEventApprovalRequested, approval("nosession", "editor")))
	waitFor(t, func() bool { return len(approvalReplies(t, r.hub)) == 1 }, "the request is answered")
	reply := approvalReplies(t, r.hub)[0]
	assert.Equal(t, "nosession", reply.ApprovalId)
	assert.False(t, reply.Approved, "Auto-approve does not approve a request of no session")
	assert.Equal(t, "The request states no session.", reply.Reason)
	assert.Zero(t, r.sink.PublishedControlCount())

	// A question of no session is refused too, when it asks this client, and
	// left to its client otherwise.
	foreign := question(r, "q-other")
	foreign["targetClientId"] = "someone-else"
	r.agent.HandleOutput(eventEnvelope(t, "", contracts.ClineEventCapabilityRequested, foreign))
	r.agent.HandleOutput(eventEnvelope(t, "", contracts.ClineEventCapabilityRequested, question(r, "q-mine")))
	waitFor(t, func() bool { return len(capabilityReplies(t, r.hub)) == 1 }, "the question is answered")
	assert.Equal(t, contracts.ClineCapabilityReply{RequestId: "q-mine", Error: "The request states no session."}, capabilityReplies(t, r.hub)[0])
	assert.Zero(t, r.sink.PublishedControlCount())
}

// The dispatcher hands every event over in order, and the reader waits when the
// queue is full. An automatic answer that the dispatcher waited for would then
// wait for a reply that the waiting reader cannot read, until the request's
// timeout. So an automatic answer leaves the dispatcher, and the stream flows on.
func TestAnAutomaticAnswerDoesNotStallTheStream(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.APITimeout = 10 * time.Minute
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	flooded := make(chan struct{})
	answered := make(chan struct{})
	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
		// Reply only once the queue is full, so the reply queues behind the
		// flood, as it does when the daemon streams fast.
		<-flooded
		close(answered)
		return fakeReply{}
	})
	r.emit(contracts.ClineEventApprovalRequested, approval("auto-1", "editor"))
	for i := range hubQueueDepth + 4 {
		r.emit("probe.noop", map[string]any{"i": i})
	}
	close(flooded)
	r.emit(contracts.ClineEventSessionNotice, map[string]any{"message": "The stream flows.", "agent": map[string]any{"kind": agentKindLead}})
	<-answered
	waitFor(t, func() bool { return r.sink.NotificationCount() >= 1 }, "every event after the approval reaches the agent")
}
