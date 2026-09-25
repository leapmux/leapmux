package mimo

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionAskedEvent(t *testing.T, id, sessionID, messageID string) []byte {
	t.Helper()
	properties := map[string]any{
		"id": id, "sessionID": sessionID, "permission": "bash", "patterns": []string{"rm -rf build"},
		"metadata": map[string]any{"command": "rm -rf build"}, "always": []string{"rm *"},
	}
	if messageID != "" {
		properties["tool"] = map[string]any{"messageID": messageID, "callID": "call-1"}
	}
	return eventJSON(t, contracts.MiMoEventPermissionAsked, properties)
}

func questionAskedEvent(t *testing.T, id, sessionID string) []byte {
	t.Helper()
	return eventJSON(t, contracts.OpenCodeEventQuestionAsked, map[string]any{
		"id": id, "sessionID": sessionID,
		"questions": []any{map[string]any{"question": "Which database?", "header": "Database",
			"options": []any{map[string]any{"label": "SQLite"}, map[string]any{"label": "Postgres"}}}},
		"tool": map[string]any{"messageID": "msg_1", "callID": "call-q"},
	})
}

func planAskedEvent(t *testing.T, id, planPath string) []byte {
	t.Helper()
	return eventJSON(t, contracts.OpenCodeEventQuestionAsked, map[string]any{
		"id": id, "sessionID": testSessionID,
		"questions": []any{map[string]any{"key": mimoQuestionKeyPlanExit, "question": "Approve the plan?",
			"params":  map[string]string{"plan": planPath},
			"options": []any{map[string]any{"label": "Yes"}, map[string]any{"label": "No"}}}},
		"tool": map[string]any{"messageID": "msg_1", "callID": "call-plan"},
	})
}

// elicitationAskedEvent is an MCP server's confirmation as MiMo HEAD asks it: one
// question with MiMo's three answers, no free text, and no tool call.
func elicitationAskedEvent(t *testing.T, id string) []byte {
	t.Helper()
	return eventJSON(t, contracts.OpenCodeEventQuestionAsked, map[string]any{
		"id": id, "sessionID": testSessionID,
		"questions": []any{map[string]any{"key": "mcp_elicitation", "header": "docs", "question": "docs\n\nProceed?",
			"options": []any{
				map[string]any{"label": elicitationAnswerAccept, "description": ""},
				map[string]any{"label": elicitationAnswerDecline, "description": ""},
				map[string]any{"label": elicitationAnswerCancel, "description": ""},
			},
			"multiple": false, "custom": false}},
	})
}

func decodePayload(t *testing.T, payload []byte) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &fields))
	return fields
}

func TestPermissionRequestIsPublished(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)
	asked := permissionAskedEvent(t, "per_1", testSessionID, "msg_1")

	feed(a, messageEvent(t, "msg_1", roleAssistant, mainActorID, false), asked)
	require.Equal(t, 1, sink.PublishedControlCount())
	published := sink.LastPublishedControl()
	assert.Equal(t, "mimo-permission:per_1", published.RequestID)
	fields := decodePayload(t, published.Payload)
	assert.JSONEq(t, `"permission.asked"`, string(fields["type"]))
	var event mimoEvent
	require.NoError(t, json.Unmarshal(asked, &event))
	assert.JSONEq(t, string(event.Properties), string(fields["properties"]), "the browser reads MiMo's own event")
	assert.JSONEq(t, `{"tool_name":"bash","tool_use_id":"call-1","input":{"command":"rm -rf build"}}`, string(fields["request"]))
	assert.NotContains(t, fields, contracts.MiMoControlFieldPlan)
}

// The reconnect path lists every pending request again. An identical request
// is published with the same bytes, so the service keeps the user's open card.
func TestRepublishedRequestKeepsItsPayload(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)
	asked := permissionAskedEvent(t, "per_1", testSessionID, "")

	feed(a, asked, asked)
	published := sink.PublishedControls()
	require.Len(t, published, 2)
	assert.Equal(t, published[0].Payload, published[1].Payload)
	assert.Len(t, a.controls, 1)
}

func TestRequestOfASessionTheAgentLeftIsRefused(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)

	feed(a, permissionAskedEvent(t, "per_1", "ses_left", ""), questionAskedEvent(t, "que_1", "ses_left"))
	assert.Zero(t, sink.PublishedControlCount(), "nobody can see a request of a session the agent left")
	waitFor(t, func() bool {
		return len(server.requestsTo("POST /permission/per_1/reply")) == 1 && len(server.requestsTo("POST /question/que_1/reject")) == 1
	}, "a refusal frees the turn that the request blocks")
	assert.JSONEq(t, `{"reply":"reject","message":"LeapMux no longer shows this session."}`,
		string(server.requestsTo("POST /permission/per_1/reply")[0].Body))
}

// A request that states no id cannot be answered, so it is neither published
// nor refused.
func TestUnreadableRequestsAreDropped(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)

	feed(a,
		eventJSON(t, contracts.MiMoEventPermissionAsked, map[string]any{"sessionID": testSessionID, "permission": "bash"}),
		eventJSON(t, contracts.MiMoEventPermissionAsked, "not an object"),
		eventJSON(t, contracts.OpenCodeEventQuestionAsked, map[string]any{"sessionID": testSessionID, "questions": []any{}}),
		eventJSON(t, contracts.OpenCodeEventQuestionAsked, []string{"not", "an", "object"}),
		eventJSON(t, eventBashInteractiveAsked, map[string]any{"sessionID": testSessionID, "command": "vim"}),
		eventJSON(t, eventPermissionReplied, map[string]any{"sessionID": testSessionID}),
		eventJSON(t, contracts.OpenCodeEventQuestionRejected, "not an object"),
	)
	assert.Zero(t, sink.PublishedControlCount())
	assert.Empty(t, sink.CanceledControls())
	assert.Empty(t, a.controls)
	assert.Empty(t, server.allRequests())
}

// A request that states no tool call is the main agent's, so the main turn end
// retires it.
func TestRequestWithoutAToolCallIsTheMainAgents(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	feed(a, eventJSON(t, contracts.MiMoEventPermissionAsked, map[string]any{
		"id": "per_1", "sessionID": testSessionID, "permission": "webfetch", "tool": map[string]any{"callID": "call-9"},
	}))
	require.Contains(t, a.controls, "mimo-permission:per_1")
	assert.Equal(t, mainActorID, a.controls["mimo-permission:per_1"].actorID, "a call with no message is taken as the main agent's")
	assert.JSONEq(t, `{"tool_name":"webfetch","tool_use_id":"call-9"}`, string(decodePayload(t, sink.LastPublishedControl().Payload)["request"]))

	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"mimo-permission:per_1"}, sink.CanceledControls())
}

func TestFailedPublicationRefusesTheRequest(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	sink.PublicationError = errors.New("database is closed")

	feed(a, permissionAskedEvent(t, "per_1", testSessionID, ""), questionAskedEvent(t, "que_1", testSessionID))
	waitFor(t, func() bool {
		return len(server.requestsTo("POST /permission/per_1/reply")) == 1 && len(server.requestsTo("POST /question/que_1/reject")) == 1
	}, "MiMo blocks on a request that nobody can see")
	assert.Empty(t, a.controls)
}

func TestQuestionIsPublished(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)

	feed(a, messageEvent(t, "msg_1", roleAssistant, mainActorID, false), questionAskedEvent(t, "que_1", testSessionID))
	published := sink.LastPublishedControl()
	assert.Equal(t, "mimo-question:que_1", published.RequestID)
	fields := decodePayload(t, published.Payload)
	assert.JSONEq(t, `"question.asked"`, string(fields["type"]))
	assert.JSONEq(t, `{"tool_name":"question","tool_use_id":"call-q"}`, string(fields["request"]))
	assert.Zero(t, sink.PlanUpdateCount())
}

func TestPlanApprovalCarriesThePlan(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)
	// MiMo states the plan's path relative to the worktree root, which can be an
	// ancestor of the working directory.
	root := a.workingDir
	a.workingDir = filepath.Join(root, "packages", "app")
	require.NoError(t, os.MkdirAll(a.workingDir, 0o755))
	plan := "# Ship the feature\n\n1. Write the code.\n"
	agenttest.WriteFixtureFile(t, filepath.Join(root, ".mimocode", "plans", "ship.md"), plan)

	asked := planAskedEvent(t, "que_2", ".mimocode/plans/ship.md")
	feed(a, messageEvent(t, "msg_1", roleAssistant, mainActorID, false), asked)
	published := sink.LastPublishedControl()
	assert.Equal(t, "mimo-question:que_2", published.RequestID)
	fields := decodePayload(t, published.Payload)
	assert.JSONEq(t, `{"tool_name":"plan_exit","tool_use_id":"call-plan"}`, string(fields["request"]))
	var stated string
	require.NoError(t, json.Unmarshal(fields[contracts.MiMoControlFieldPlan], &stated))
	assert.Equal(t, plan, stated)

	require.Equal(t, 1, sink.PlanUpdateCount())
	update := sink.LastPlanUpdate()
	content, err := msgcodec.Decompress(update.Content, update.Compression)
	require.NoError(t, err)
	assert.Equal(t, plan, string(content))
	assert.Equal(t, "Ship the feature", update.Title)

	feed(a, asked)
	assert.Equal(t, 1, sink.PlanUpdateCount(), "a restated approval does not restate the plan")
}

func TestReadPlan(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	dir := a.workingDir
	agenttest.WriteFixtureFile(t, filepath.Join(dir, "plan.md"), "# Plan")
	agenttest.WriteFixtureFile(t, filepath.Join(dir, "upper.MD"), "# Upper")
	agenttest.WriteFixtureFile(t, filepath.Join(dir, "plan.txt"), "# Plan")
	agenttest.WriteFixtureFile(t, filepath.Join(dir, "binary.md"), "\xff\xfe")
	agenttest.WriteFixtureFile(t, filepath.Join(dir, "huge.md"), strings.Repeat("x", mimoMaxPlanBytes+1))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "folder.md"), 0o755))

	assert.Equal(t, "# Plan", a.readPlan("plan.md"))
	assert.Equal(t, "# Plan", a.readPlan(filepath.Join(dir, "plan.md")), "an absolute path is read as it stands")
	assert.Equal(t, "# Upper", a.readPlan("upper.MD"), "the extension compares without case")
	for _, path := range []string{"", "plan.txt", "binary.md", "huge.md", "folder.md", "missing.md"} {
		assert.Empty(t, a.readPlan(path), "path %q", path)
	}
}

func TestSettledRequestsRetireTheirCards(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)

	feed(a,
		permissionAskedEvent(t, "per_1", testSessionID, ""),
		questionAskedEvent(t, "que_1", testSessionID),
		questionAskedEvent(t, "que_2", testSessionID),
		eventJSON(t, eventPermissionReplied, map[string]any{"sessionID": testSessionID, "requestID": "per_1", "reply": "once"}),
		eventJSON(t, contracts.OpenCodeEventQuestionReplied, map[string]any{"sessionID": testSessionID, "requestID": "que_1", "answers": [][]string{{"SQLite"}}}),
		eventJSON(t, contracts.OpenCodeEventQuestionRejected, map[string]any{"sessionID": testSessionID, "requestID": "que_2"}),
		eventJSON(t, contracts.OpenCodeEventQuestionRejected, map[string]any{"sessionID": testSessionID, "requestID": "que_9"}),
	)
	assert.Equal(t, []string{"mimo-permission:per_1", "mimo-question:que_1", "mimo-question:que_2"}, sink.CanceledControls(),
		"another client's answer retires the card, and an unknown request retires nothing")
	assert.Empty(t, a.controls)
}

// A subagent's request belongs to the subagent's turn, which the parent's turn
// end does not end.
func TestSubagentRequestIsRetiredWithItsOwnTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newControlTestAgent(t)

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_c", roleAssistant, actorID, false),
		permissionAskedEvent(t, "per_c", testSessionID, "msg_c"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	assert.Empty(t, sink.CanceledControls())
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeCancelled, 1, ""))
	assert.Equal(t, []string{"mimo-permission:per_c"}, sink.CanceledControls())
}

func TestParseControlAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		kind    mimoControlKind
		content []byte
		want    mimoAnswer
		err     string
	}{
		{name: "an allow approves a permission once", kind: controlPermission, content: allowEnvelope("r1"),
			want: mimoAnswer{requestID: "r1", permission: mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyOnce}}},
		{name: "a deny rejects a permission with the reason", kind: controlPermission, content: denyEnvelope("r1", "Too risky"),
			want: mimoAnswer{requestID: "r1", permission: mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject, Message: "Too risky"}}},
		{name: "a chosen option answers a permission", kind: controlPermission,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`),
			want:    mimoAnswer{requestID: "r1", permission: mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyAlways}}},
		{name: "a cancelled choice rejects a permission", kind: controlPermission,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"outcome":{"outcome":"cancelled"}}}`),
			want:    mimoAnswer{requestID: "r1", permission: mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject}}},
		{name: "an unknown option is refused", kind: controlPermission,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"outcome":{"outcome":"selected","optionId":"forever"}}}`),
			err:     "unknown permission option"},
		{name: "a permission answer with no option is refused", kind: controlPermission,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{}}`), err: "states no option"},
		{name: "answers answer a question", kind: controlQuestion,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"answers":[["SQLite"],["a","b"]]}}`),
			want:    mimoAnswer{requestID: "r1", answers: [][]string{{"SQLite"}, {"a", "b"}}}},
		{name: "a rejection rejects a question", kind: controlQuestion,
			content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"rejected":true}}`), want: mimoAnswer{requestID: "r1", reject: true}},
		{name: "a deny rejects a question", kind: controlQuestion, content: denyEnvelope("r1", ""), want: mimoAnswer{requestID: "r1", reject: true}},
		{name: "an allow does not answer a question", kind: controlQuestion, content: allowEnvelope("r1"), err: "does not answer a question"},
		{name: "an empty result is refused", kind: controlQuestion, content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{}}`),
			err: "neither answers nor a rejection"},
		{name: "an allow approves a plan", kind: controlPlan, content: allowEnvelope("r1"),
			want: mimoAnswer{requestID: "r1", answers: [][]string{{planAnswerYes}}}},
		{name: "a deny with feedback keeps planning with it", kind: controlPlan, content: denyEnvelope("r1", "Add tests first"),
			want: mimoAnswer{requestID: "r1", answers: [][]string{{"Add tests first"}}}},
		{name: "a deny without feedback says no", kind: controlPlan, content: denyEnvelope("r1", ""),
			want: mimoAnswer{requestID: "r1", answers: [][]string{{planAnswerNo}}}},
		{name: "a rejection declines a plan", kind: controlPlan, content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"rejected":true}}`),
			want: mimoAnswer{requestID: "r1", answers: [][]string{{planAnswerNo}}}},
		{name: "answers do not answer a plan", kind: controlPlan, content: []byte(`{"jsonrpc":"2.0","id":"r1","result":{"answers":[["Yes"]]}}`),
			err: "takes an allow or a deny"},
		{name: "an unknown behavior is refused", kind: controlPermission,
			content: []byte(`{"response":{"request_id":"r1","response":{"behavior":"maybe"}}}`), err: "unknown behavior"},
		{name: "an accepted elicitation answers MiMo's Accept", kind: controlQuestion,
			content: elicitationEnvelope("r1", contracts.MCPElicitationActionAccept),
			want:    mimoAnswer{requestID: "r1", answers: [][]string{{elicitationAnswerAccept}}, elicitation: true}},
		{name: "a declined elicitation answers MiMo's Decline", kind: controlQuestion,
			content: elicitationEnvelope("r1", contracts.MCPElicitationActionDecline),
			want:    mimoAnswer{requestID: "r1", answers: [][]string{{elicitationAnswerDecline}}, elicitation: true}},
		{name: "a cancelled elicitation answers MiMo's Cancel", kind: controlQuestion,
			content: elicitationEnvelope("r1", contracts.MCPElicitationActionCancel),
			want:    mimoAnswer{requestID: "r1", answers: [][]string{{elicitationAnswerCancel}}, elicitation: true}},
		{name: "an unknown elicitation action is refused", kind: controlQuestion, content: elicitationEnvelope("r1", "maybe"),
			err: "unknown elicitation action"},
		{name: "an elicitation that states no request is refused", kind: controlQuestion, content: elicitationEnvelope("", contracts.MCPElicitationActionAccept),
			err: "states no request"},
		{name: "an elicitation action does not answer a permission", kind: controlPermission,
			content: elicitationEnvelope("r1", contracts.MCPElicitationActionAccept), err: "answers only a question"},
		{name: "an elicitation action does not answer a plan", kind: controlPlan,
			content: elicitationEnvelope("r1", contracts.MCPElicitationActionAccept), err: "answers only a question"},
		{name: "an answer that names no request is refused", kind: controlQuestion, content: []byte(`{"result":{"rejected":true}}`),
			err: "states no request"},
		{name: "bytes that are not JSON are refused", kind: controlQuestion, content: []byte(`nope`), err: "states no request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseControlAnswer(tc.kind, tc.content)
			if tc.err != "" {
				assert.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Feedback that reads exactly as an option would be taken as that option, and
// "Yes" would approve the plan that the user rejected.
func TestPlanFeedback(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"":              planAnswerNo,
		"   ":           planAnswerNo,
		"Yes":           "Yes.",
		" No ":          "No.",
		"yes":           "yes",
		"Add the tests": "Add the tests",
	} {
		assert.Equal(t, want, planFeedback(input), "input %q", input)
	}
}

func TestExecuteControlReply(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		asked  func(t *testing.T) []byte
		answer []byte
		route  string
		body   string
	}{
		{name: "a permission approved once", asked: func(t *testing.T) []byte { return permissionAskedEvent(t, "per_1", testSessionID, "") },
			answer: allowEnvelope("mimo-permission:per_1"), route: "POST /permission/per_1/reply", body: `{"reply":"once"}`},
		{name: "a permission rejected with a reason", asked: func(t *testing.T) []byte { return permissionAskedEvent(t, "per_1", testSessionID, "") },
			answer: denyEnvelope("mimo-permission:per_1", "Not now"), route: "POST /permission/per_1/reply", body: `{"reply":"reject","message":"Not now"}`},
		{name: "a permission approved always", asked: func(t *testing.T) []byte { return permissionAskedEvent(t, "per_1", testSessionID, "") },
			answer: []byte(`{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`),
			route:  "POST /permission/per_1/reply", body: `{"reply":"always"}`},
		{name: "a question answered", asked: func(t *testing.T) []byte { return questionAskedEvent(t, "que_1", testSessionID) },
			answer: []byte(`{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["Postgres"]]}}`),
			route:  "POST /question/que_1/reply", body: `{"answers":[["Postgres"]]}`},
		{name: "a question dismissed", asked: func(t *testing.T) []byte { return questionAskedEvent(t, "que_1", testSessionID) },
			answer: []byte(`{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"rejected":true}}`), route: "POST /question/que_1/reject"},
		{name: "a plan rejected with feedback", asked: func(t *testing.T) []byte { return planAskedEvent(t, "que_2", "missing.md") },
			answer: denyEnvelope("mimo-question:que_2", "Split step 2"), route: "POST /question/que_2/reply", body: `{"answers":[["Split step 2"]]}`},
		{name: "an MCP server's confirmation accepted", asked: func(t *testing.T) []byte { return elicitationAskedEvent(t, "que_3") },
			answer: elicitationEnvelope("mimo-question:que_3", contracts.MCPElicitationActionAccept),
			route:  "POST /question/que_3/reply", body: `{"answers":[["Accept"]]}`},
		{name: "an MCP server's confirmation cancelled", asked: func(t *testing.T) []byte { return elicitationAskedEvent(t, "que_3") },
			answer: elicitationEnvelope("mimo-question:que_3", contracts.MCPElicitationActionCancel),
			route:  "POST /question/que_3/reply", body: `{"answers":[["Cancel"]]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newControlTestAgent(t)
			feed(a, tc.asked(t))

			require.NoError(t, a.SendRawInput(tc.answer))
			requests := server.requestsTo(tc.route)
			require.Len(t, requests, 1)
			if tc.body != "" {
				assert.JSONEq(t, tc.body, string(requests[0].Body))
			}
			assert.Empty(t, a.controls, "an answered request is forgotten")
			assert.Empty(t, sink.PermissionMode(), "only an approved plan moves the session")
			assert.ErrorContains(t, a.SendRawInput(tc.answer), "no pending request", "a request takes one answer")
		})
	}
}

// MiMo moves the session to build as it takes the approval. The next prompt
// must state build too, or it would put the session back into plan mode.
func TestApprovedPlanAdoptsBuild(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	a.mode = contracts.MiMoModePlan
	feed(a, planAskedEvent(t, "que_2", "missing.md"))

	require.NoError(t, a.SendRawInput(allowEnvelope("mimo-question:que_2")))
	assert.JSONEq(t, `{"answers":[["Yes"]]}`, string(server.requestsTo("POST /question/que_2/reply")[0].Body))
	assert.Equal(t, contracts.MiMoModeBuild, a.mode)
	assert.Equal(t, contracts.MiMoModeBuild, sink.PermissionMode())

	require.NoError(t, a.SendInput("go", nil))
	assert.Equal(t, "build", decodeBody(t, server.requestsTo("POST /session/ses_test/prompt_async")[0])["agent"])
}

func TestAnswerToARequestMiMoDroppedRetiresTheCard(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	feed(a, permissionAskedEvent(t, "per_1", testSessionID, ""))
	server.respond("POST /permission/per_1/reply", http.StatusNotFound, `{"name":"NotFoundError"}`)

	err := a.SendRawInput(allowEnvelope("mimo-permission:per_1"))
	assert.ErrorContains(t, err, "no longer waits")
	assert.Equal(t, []string{"mimo-permission:per_1"}, sink.CanceledControls())
	assert.Empty(t, a.controls)
}

func TestAnswerThatFailsToSendKeepsTheRequest(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	feed(a, permissionAskedEvent(t, "per_1", testSessionID, ""))
	server.respond("POST /permission/per_1/reply", http.StatusInternalServerError, `{}`)

	assert.ErrorContains(t, a.SendRawInput(allowEnvelope("mimo-permission:per_1")), "send the answer")
	assert.Empty(t, sink.CanceledControls())
	assert.Len(t, a.controls, 1, "the user can answer again")
}

func TestAnswerThatDoesNotFitIsRefused(t *testing.T) {
	t.Parallel()
	a, _, server := newControlTestAgent(t)
	feed(a, questionAskedEvent(t, "que_1", testSessionID))

	assert.ErrorContains(t, a.SendRawInput(allowEnvelope("mimo-question:que_1")), "does not answer a question")
	assert.Empty(t, server.requestsTo("POST /question/que_1/reply"))
	assert.Len(t, a.controls, 1)
}

// An elicitation action becomes MiMo's own word for it, and only a question that
// offers that word can take it. An ordinary question would read "Accept" as the
// reader's choice.
func TestElicitationAnswerToAnOrdinaryQuestionIsRefused(t *testing.T) {
	t.Parallel()
	a, _, server := newControlTestAgent(t)
	feed(a, questionAskedEvent(t, "que_1", testSessionID))

	err := a.SendRawInput(elicitationEnvelope("mimo-question:que_1", contracts.MCPElicitationActionAccept))
	assert.ErrorContains(t, err, `does not offer "Accept"`)
	assert.Empty(t, server.requestsTo("POST /question/que_1/reply"))
	assert.Len(t, a.controls, 1, "the question still waits for an answer")
}

// An interactive command waits for keyboard input that nobody can type here.
// The worker refuses it at once, so the turn goes on, and stores nothing of it:
// the event carries the command's whole shell environment.
func TestInteractiveCommandIsRefused(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)

	feed(a, eventJSON(t, eventBashInteractiveAsked, map[string]any{
		"id": "0b5d6a4e-9c0f", "sessionID": testSessionID, "command": "npm init", "description": "Create the package",
		"env": map[string]string{"AWS_SECRET_ACCESS_KEY": "do-not-store"},
	}))
	waitFor(t, func() bool { return len(server.requestsTo("POST /bash-interactive/0b5d6a4e-9c0f/reply")) == 1 }, "the refusal is sent")
	var reply mimoBashReplyBody
	require.NoError(t, json.Unmarshal(server.requestsTo("POST /bash-interactive/0b5d6a4e-9c0f/reply")[0].Body, &reply))
	assert.Equal(t, interactiveRefusalExitCode, reply.ExitCode)
	assert.Equal(t, interactiveRefusal, reply.Output)
	assert.Zero(t, sink.PublishedControlCount())
	assert.Empty(t, sink.Messages())
	assert.Zero(t, sink.NotificationCount())
}

func TestRestatePendingControls(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	feed(a, questionAskedEvent(t, "que_old", testSessionID), permissionAskedEvent(t, "per_kept", testSessionID, ""))

	var kept, raised mimoEvent
	require.NoError(t, json.Unmarshal(permissionAskedEvent(t, "per_kept", testSessionID, ""), &kept))
	require.NoError(t, json.Unmarshal(questionAskedEvent(t, "que_new", testSessionID), &raised))
	server.respond("GET /permission", http.StatusOK, "["+string(kept.Properties)+"]")
	server.respond("GET /question", http.StatusOK, "["+string(raised.Properties)+"]")
	server.respond("GET /bash-interactive", http.StatusOK, `[{"id":"0b5d6a4e","sessionID":"ses_test","command":"vim"}]`)

	a.restatePendingControls(a.Context())
	assert.Equal(t, []string{"mimo-question:que_old"}, sink.CanceledControls(), "a card whose request MiMo no longer holds goes")
	assert.ElementsMatch(t, []string{"mimo-permission:per_kept", "mimo-question:que_new"}, sortedKeys(a.controls))
	waitFor(t, func() bool {
		return len(server.requestsTo("POST /question/que_old/reject")) == 1 && len(server.requestsTo("POST /bash-interactive/0b5d6a4e/reply")) == 1
	}, "the retired question is rejected, and an interactive command raised while the stream was down is refused")
}

// A list that cannot be read states nothing about what MiMo holds, so no card
// is retired for it.
func TestRestatePendingControlsKeepsCardsWhenAListFails(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	feed(a, questionAskedEvent(t, "que_1", testSessionID), permissionAskedEvent(t, "per_1", testSessionID, ""))
	server.respond("GET /permission", http.StatusInternalServerError, `{}`)
	server.respond("GET /question", http.StatusOK, `[]`)

	a.restatePendingControls(a.Context())
	assert.Equal(t, []string{"mimo-question:que_1"}, sink.CanceledControls())
	assert.Equal(t, []string{"mimo-permission:per_1"}, sortedKeys(a.controls))
}

// The worker reads the two lists separately. A question list that cannot be
// read keeps every question card, and the permission list still retires the
// permissions that MiMo dropped.
func TestRestatePendingControlsKeepsQuestionsWhenTheirListFails(t *testing.T) {
	t.Parallel()
	a, sink, server := newControlTestAgent(t)
	feed(a, questionAskedEvent(t, "que_1", testSessionID), permissionAskedEvent(t, "per_1", testSessionID, ""))
	server.respond("GET /permission", http.StatusOK, `[]`)
	server.respond("GET /question", http.StatusInternalServerError, `{}`)
	server.respond("GET /bash-interactive", http.StatusInternalServerError, `{}`)

	a.restatePendingControls(a.Context())
	assert.Equal(t, []string{"mimo-permission:per_1"}, sink.CanceledControls())
	assert.Equal(t, []string{"mimo-question:que_1"}, sortedKeys(a.controls))
	assert.Empty(t, server.requestsTo("POST /permission/per_1/reply"), "MiMo no longer holds the permission, so nothing answers it")
}

func TestAnswerFitsRequest(t *testing.T) {
	t.Parallel()
	accept := mimoAnswer{requestID: "r1", answers: [][]string{{elicitationAnswerAccept}}, elicitation: true}

	assert.NoError(t, answerFitsRequest(accept, []byte(storedElicitationPayload)))
	assert.NoError(t, answerFitsRequest(mimoAnswer{requestID: "r1", answers: [][]string{{"SQLite"}}}, []byte(`nope`)),
		"an answer that is no elicitation needs no check")

	twoQuestions := `{"type":"question.asked","properties":{"questions":[` +
		`{"options":[{"label":"Accept"}]},{"options":[{"label":"Accept"}]}]}}`
	assert.ErrorContains(t, answerFitsRequest(accept, []byte(twoQuestions)), `does not offer "Accept"`,
		"an elicitation asks one question, so two questions are an ordinary question form")
	assert.ErrorContains(t, answerFitsRequest(accept, []byte(`nope`)), "read the stored request")
}

func TestControlKindOfPayload(t *testing.T) {
	t.Parallel()
	for payload, want := range map[string]mimoControlKind{
		storedPermissionPayload: controlPermission,
		storedQuestionPayload:   controlQuestion,
		storedPlanPayload:       controlPlan,
	} {
		kind, ok := controlKindOfPayload([]byte(payload))
		assert.True(t, ok)
		assert.Equal(t, want, kind)
	}
	_, ok := controlKindOfPayload([]byte(`{"type":"bash.interactive.asked"}`))
	assert.False(t, ok)
	_, ok = controlKindOfPayload([]byte(`nope`))
	assert.False(t, ok)
}
