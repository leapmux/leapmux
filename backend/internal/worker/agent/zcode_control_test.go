package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeRequestLine renders one server-to-client REQUEST: an id AND a method, which is
// the shape that distinguishes a request from a notification in either direction.
func zcodeRequestLine(t *testing.T, id int64, method, params string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id":     id,
		"method": method,
		"params": json.RawMessage(params),
	})
	require.NoError(t, err)
	return line
}

// zcodeStoredPayload decodes the one control request the agent persisted.
func zcodeStoredPayload(t *testing.T, sink *recordingControlSink) []byte {
	t.Helper()
	persisted := sink.PersistedControls()
	require.Len(t, persisted, 1, "a control request must be persisted exactly once")
	broadcast := sink.BroadcastControls()
	require.Len(t, broadcast, 1, "and broadcast exactly once")
	assert.Equal(t, persisted[0].RequestID, broadcast[0].RequestID)
	assert.Equal(t, persisted[0].ClaimToken, broadcast[0].ClaimToken,
		"the broadcast must carry the claim token the persist minted, or no client can claim it")
	return persisted[0].Payload
}

// zcodeAnswer builds the frontend's neutral answer envelope for a control request:
// ControlBehaviorEnvelope, with the AskUserQuestion answers where the shared control
// attaches them.
func zcodeAnswer(t *testing.T, requestID, behavior, message string, answers map[string]string) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	if message != "" {
		inner["message"] = message
	}
	if answers != nil {
		inner["updatedInput"] = map[string]any{"answers": answers}
	}
	content, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"request_id": requestID,
			"response":   inner,
		},
	})
	require.NoError(t, err)
	return content
}

// zcodeDecodedReply is the reply frame as it appears ON THE WIRE, which is what a
// test asserts on: the result arrives as bytes, not as the `any` the sender held.
type zcodeDecodedReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
}

// zcodeResolve runs the answer through the provider plugin and returns the frame the
// worker would forward to the app-server's stdin.
func zcodeResolve(t *testing.T, stored, answer []byte) (zcodeDecodedReply, ControlResponseResolution) {
	t.Helper()
	res := zcodeProvider{}.ResolveControlResponse(ControlResponseContext{
		RequestPayload:  stored,
		ResponseContent: answer,
	})
	if res.Content == nil {
		return zcodeDecodedReply{}, res
	}
	var frame zcodeDecodedReply
	require.NoError(t, json.Unmarshal(res.Content, &frame))
	return frame, res
}

// --- permission requests ---

const zcodePermissionParams = `{
  "requestId": "req-1",
  "sessionId": "sess-1",
  "toolCallId": "call-1",
  "toolName": "Bash",
  "riskLevel": "high",
  "reason": "runs a command",
  "input": {"command": "rm -rf build"},
  "options": [
    {"optionId": "allow_once", "kind": "allow", "name": "Allow once",
     "response": {"decision": "allow", "scope": "once"}},
    {"optionId": "allow_always", "kind": "allow", "name": "Always allow",
     "response": {"decision": "allow", "permissionUpdates": [{"rule": "Bash(rm:*)"}]}},
    {"optionId": "deny", "kind": "deny", "name": "Deny",
     "response": {"decision": "deny"}}
  ]
}`

// The stored payload is a hybrid on purpose: the Claude-shaped header the SHARED
// control surfaces read, plus ZCode's own params, plus the wire id the reply needs.
func TestHandlePermissionRequest_StoresTheHybridPayload(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))

	payload := zcodeStoredPayload(t, sink)
	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(payload, &stored))
	assert.Equal(t, "control_request", stored.Type)
	assert.Equal(t, "req-1", stored.RequestID)
	assert.Equal(t, ZCodeMethodRequestPermission, stored.Method)
	assert.Equal(t, "Bash", stored.Request.ToolName)
	assert.Equal(t, "call-1", stored.Request.ToolUseID)
	assert.JSONEq(t, `{"command":"rm -rf build"}`, string(stored.Request.Input))
	assert.JSONEq(t, zcodePermissionParams, string(stored.Params),
		"ZCode's own params must survive verbatim, because the answer path reads the option list off them")

	assert.Equal(t, "7", string(stored.WireID))
	extracted, _, ok := ExtractJSONRPCID(payload)
	require.True(t, ok, "the wire id must be spelled `id`, or the shared extractor cannot find it")
	assert.Equal(t, "7", string(extracted))

	// Nothing is answered yet: the whole point of a prompt is that the app-server waits.
	assert.Empty(t, a.stdin.(*zcodeRecordedStdin).Frames())
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.NotEmpty(t, a.pendingControls["req-1"],
		"the prompt's payload is remembered so a re-announcement republishes THESE bytes, and so its "+
			"permission.resolved echo is not read as an automatic decision")
}

// An allow echoes the CHOSEN option's own response, because it carries the
// permission-rule updates an "always allow" implies. Rebuilding a bare decision
// would drop them and re-prompt on the next identical call.
func TestZCodeResolveControlResponse_AllowEchoesTheOptionResponse(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	frame, res := zcodeResolve(t, stored, zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil))
	assert.Equal(t, "7", string(frame.ID))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeDecisionAllow, result["decision"])
	assert.Equal(t, "once", result["scope"], "the first allowing option's response is echoed whole")
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestZCodeResolveControlResponse_DenyCarriesTheUsersReason(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	frame, _ := zcodeResolve(t, stored, zcodeAnswer(t, "req-1", ControlBehaviorDeny, "not on my machine", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeDecisionDeny, result["decision"])
	assert.Equal(t, "not on my machine", result["reason"],
		"the typed reason must reach the model, or it retries the same call")
}

// Fail-safe: an option list that offers no allow cannot produce one. Granting
// something no offered option covers is the one outcome a permission prompt must
// never have.
func TestZCodePermissionResult_AnAllowWithNoAllowingOptionDenies(t *testing.T) {
	t.Parallel()

	options := []zcodePermissionOption{
		{OptionID: "deny", Response: json.RawMessage(`{"decision":"deny"}`)},
		{OptionID: "weird", Response: json.RawMessage(`{"decision":"escalate"}`)},
	}
	raw, err := zcodePermissionResult(options, ControlBehaviorAllow, "")
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, contracts.ZCodeDecisionDeny, result["decision"])
	assert.Equal(t, "no offered option allows this tool call", result["reason"])
}

// With NO options at all there is nothing to be fail-safe about: the app-server left
// the decision entirely to the client, so a bare decision is the whole answer.
func TestZCodePermissionResult_WithNoOptionsSynthesizesABareDecision(t *testing.T) {
	t.Parallel()

	for _, behavior := range []string{ControlBehaviorAllow, ControlBehaviorDeny} {
		raw, err := zcodePermissionResult(nil, behavior, "because")
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.Unmarshal(raw, &result))
		want := contracts.ZCodeDecisionAllow
		if behavior != ControlBehaviorAllow {
			want = contracts.ZCodeDecisionDeny
		}
		assert.Equal(t, want, result["decision"])
		assert.Equal(t, "because", result["reason"])
	}
}

// An option whose response is absent or unparseable cannot be echoed. It must not
// match, so the fail-safe path decides instead of a malformed frame going out.
func TestZCodeOptionForDecision_SkipsAnUnusableResponse(t *testing.T) {
	t.Parallel()

	options := []zcodePermissionOption{
		{OptionID: "no-response"},
		{OptionID: "bad-json", Response: json.RawMessage(`{"decision":`)},
		{OptionID: "good", Response: json.RawMessage(`{"decision":"allow"}`)},
	}
	option := zcodeOptionForDecision(options, contracts.ZCodeDecisionAllow)
	require.NotNil(t, option)
	assert.Equal(t, "good", option.OptionID)

	assert.Nil(t, zcodeOptionForDecision(options[:2], contracts.ZCodeDecisionAllow))
	assert.Nil(t, zcodeOptionForDecision(nil, contracts.ZCodeDecisionDeny))
}

// A reason is merged INTO the echoed response, so the rule updates survive alongside it.
func TestZCodeOptionResponse_MergesTheReasonWithoutLosingTheOptionsFields(t *testing.T) {
	t.Parallel()

	option := zcodePermissionOption{Response: json.RawMessage(`{"decision":"deny","scope":"session"}`)}
	raw, err := zcodeOptionResponse(option, contracts.ZCodeDecisionDeny, "no")
	require.NoError(t, err)
	assert.JSONEq(t, `{"decision":"deny","scope":"session","reason":"no"}`, string(raw))

	// A response that is not an object cannot take a reason. A bare decision that
	// carries it is better than dropping the user's words.
	scalar := zcodePermissionOption{Response: json.RawMessage(`"allow"`)}
	raw, err = zcodeOptionResponse(scalar, contracts.ZCodeDecisionDeny, "no")
	require.NoError(t, err)
	assert.JSONEq(t, `{"decision":"deny","reason":"no"}`, string(raw))
}

// A prompt with no request id can never be answered, so it must not be left pending:
// the app-server would block the turn for good. Denying lets the model report it.
func TestHandlePermissionRequest_NoRequestIDDeniesImmediately(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)

	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission,
		`{"toolName":"Bash","options":[{"optionId":"deny","response":{"decision":"deny"}}]}`))

	assert.Empty(t, sink.PersistedControls(), "an unanswerable prompt must not become a pending row")
	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, "7", string(requests[0].ID))
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, contracts.ZCodeDecisionDeny, result["decision"])
}

// A request LeapMux could not even read gets an ERROR, not a denial: a denial is a
// decision, and LeapMux made none.
func TestHandlePermissionRequest_MalformedParamsAnswerWithAnError(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)

	a.HandleOutput([]byte(`{"id":7,"method":"interaction/requestPermission","params":"not an object"}`))

	assert.Empty(t, sink.PersistedControls())
	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.NotNil(t, requests[0].Error)
	assert.Equal(t, ZCodeErrInternal, requests[0].Error.Code)
	assert.Contains(t, requests[0].Error.Message, "could not read")
}

// --- plan approval ---

const zcodePlanParams = `{
  "requestId": "req-plan",
  "sessionId": "sess-1",
  "toolCallId": "call-plan",
  "prompt": "Review this implementation plan.",
  "schema": {"interaction": "plan_approval"},
  "context": {"plan": "1. read\n2. write"}
}`

func TestHandleUserInputRequest_PlanApprovalIsStoredAsAPlanControl(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 9, ZCodeMethodRequestUserInput, zcodePlanParams))

	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(zcodeStoredPayload(t, sink), &stored))
	assert.Equal(t, contracts.ZCodeToolNameExitPlanMode, stored.Request.ToolName,
		"the plan surface is selected by tool name, so it must be ZCode's plan tool")
	assert.Equal(t, PlanModeControlExit, zcodeProvider{}.PlanModeControl(stored.Request.ToolName))

	// A plan approval carries no questions, so one is synthesized: the shared surface
	// needs a question to key the answer by.
	var input struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Value string `json:"value"`
			} `json:"options"`
		} `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(stored.Request.Input, &input))
	require.Len(t, input.Questions, 1)
	assert.Equal(t, ZCodePlanApprovalQuestion, input.Questions[0].Question)
	require.Len(t, input.Questions[0].Options, 1)
	assert.Equal(t, ZCodePlanApproveSentinel, input.Questions[0].Options[0].Value)

	// The plan text travels as its own field. The synthesized question above is
	// boilerplate, and the frontend reads the first question text -- so a plan left in
	// `context` would never reach the banner, which would render that boilerplate as
	// the plan.
	var withPlan struct {
		Plan string `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(stored.Request.Input, &withPlan))
	assert.Equal(t, "1. read\n2. write", withPlan.Plan)

	// NO planModeToolUse entry. That map has exactly one reader,
	// ClaudeCodeAgent.detectPlanModeFromToolResult, which no ZCode path reaches -- and
	// the shared code deletes an entry only on an ALLOW, so a denied or abandoned plan
	// left one in a process-global map for the worker's life.
	_, ok := sink.LoadAndDeletePlanModeToolUse("call-plan")
	assert.False(t, ok, "nothing reads it for ZCode, and only an allow would ever remove it")
}

// The sentinel is the ONLY value the app-server's plan reader accepts as approval.
func TestZCodeResolveControlResponse_PlanAcceptSendsTheSentinel(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 9, ZCodeMethodRequestUserInput, zcodePlanParams))

	frame, res := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", ControlBehaviorAllow, "", nil))
	assert.Equal(t, PlanModeControlExit, res.PlanModeControl)
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, ZCodeActionAccept, result[ZCodeReplyActionField])
	content, ok := result[ZCodeReplyContentField].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, ZCodePlanApproveSentinel, content[ZCodeAnswerField])
}

// A plan rejection WITH feedback goes out as an accept whose answer is the feedback,
// because the plan path's reply mapper drops every field but `action` -- a decline
// would silently lose the text the user typed.
func TestZCodeResolveControlResponse_PlanRejectionWithFeedbackKeepsTheText(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 9, ZCodeMethodRequestUserInput, zcodePlanParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", ControlBehaviorDeny, "  split step 2  ", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, ZCodeActionAccept, result[ZCodeReplyActionField])
	content, ok := result[ZCodeReplyContentField].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "split step 2", content[ZCodeAnswerField], "the feedback is trimmed, not dropped")
	assert.NotEqual(t, ZCodePlanApproveSentinel, content[ZCodeAnswerField])
}

// With nothing typed there is no text to preserve, so the honest reply is a decline.
func TestZCodeResolveControlResponse_PlanRejectionWithNoFeedbackDeclines(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 9, ZCodeMethodRequestUserInput, zcodePlanParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", ControlBehaviorDeny, "   ", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, ZCodeActionDecline, result[ZCodeReplyActionField])
	assert.NotContains(t, result, ZCodeReplyContentField)
}

// --- AskUserQuestion ---

const zcodeQuestionParams = `{
  "requestId": "req-q",
  "sessionId": "sess-1",
  "toolCallId": "call-q",
  "schema": {
    "interaction": "ask_user_question",
    "questions": [
      {"question": "Which database?", "header": "Storage", "multiSelect": false,
       "options": [{"value": "pg", "label": "Postgres"}, {"value": "sqlite", "label": "SQLite"}]},
      {"question": "Which extras?", "multiSelect": true,
       "options": [{"value": "cache"}, {"label": "Metrics"}]}
    ]
  }
}`

func TestHandleUserInputRequest_QuestionIsStoredForTheSharedControl(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(zcodeStoredPayload(t, sink), &stored))
	assert.Equal(t, contracts.ZCodeToolNameAskUserQuestion, stored.Request.ToolName,
		"the shared AskUserQuestion control is selected by this exact tool name")

	var input struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Value string `json:"value"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(stored.Request.Input, &input))
	require.Len(t, input.Questions, 2)
	assert.Equal(t, "Which database?", input.Questions[0].Question)
	assert.Equal(t, "Storage", input.Questions[0].Header)
	assert.False(t, input.Questions[0].MultiSelect)
	assert.True(t, input.Questions[1].MultiSelect)

	// A one-sided option is completed from the other side, so no option renders blank
	// and no answer is unaddressable.
	require.Len(t, input.Questions[1].Options, 2)
	assert.Equal(t, "cache", input.Questions[1].Options[0].Label, "a value with no label labels itself")
	assert.Equal(t, "Metrics", input.Questions[1].Options[1].Value, "a label with no value values itself")

	// A question is not a plan approval, so nothing is recorded for the plan machinery.
	_, ok := sink.LoadAndDeletePlanModeToolUse("call-q")
	assert.False(t, ok)
}

// The reply carries the keyed map AND the positional form together: the keyed map is
// exact when the question text survived, and the positional form answers a question
// whose text did not.
func TestZCodeResolveControlResponse_QuestionAnswersCarryBothKeyForms(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-q", ControlBehaviorAllow, "", map[string]string{
			"Which database?": "Postgres",
			"Which extras?":   "cache, Metrics",
		}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, ZCodeActionAccept, result[ZCodeReplyActionField])
	content, ok := result[ZCodeReplyContentField].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "Postgres", content["answer_0"])
	assert.Equal(t, "cache, Metrics", content["answer_1"])
	answers, ok := content[ZCodeAnswerMapField].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Postgres", answers["Which database?"])
	assert.Equal(t, "cache, Metrics", answers["Which extras?"])
}

// A question the user skipped must be absent from BOTH forms. The reader discards a
// blank answer anyway, and sending one only makes the map look answered.
func TestZCodeAnswerContent_OmitsABlankAnswerFromBothForms(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{
		Questions: []string{"first", "second", "third"},
		Answers:   map[string]string{"first": "yes", "second": "   "},
	})
	assert.Equal(t, "yes", content["answer_0"])
	assert.NotContains(t, content, "answer_1")
	assert.NotContains(t, content, "answer_2")
	answers, ok := content[ZCodeAnswerMapField].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"first": "yes"}, answers)
}

// An EMPTY answers object is not the same as an absent one: the reader short-circuits
// to "answered with nothing" and suppresses the positional fallback.
func TestZCodeAnswerContent_OmitsTheAnswersMapEntirelyWhenEmpty(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{Questions: []string{"only"}})
	assert.NotContains(t, content, ZCodeAnswerMapField)
	assert.Empty(t, content)
}

// A question whose text is empty can only be answered positionally, so the keyed map
// must not gain an empty key that matches nothing.
func TestZCodeAnswerContent_AnEmptyQuestionTextGetsOnlyThePositionalForm(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{
		Questions: []string{""},
		Answers:   map[string]string{"": "yes"},
	})
	assert.Equal(t, "yes", content["answer_0"])
	assert.NotContains(t, content, ZCodeAnswerMapField)
}

// An answer whose question the request never declared cannot be placed positionally,
// but its key can still match -- which beats dropping what the user chose.
func TestZCodeAnswerContent_AnUndeclaredQuestionKeepsItsKeyedAnswer(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{
		Questions: []string{"declared"},
		Answers:   map[string]string{"declared": "a", "surprise": "b"},
	})
	answers, ok := content[ZCodeAnswerMapField].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"declared": "a", "surprise": "b"}, answers)
	assert.Equal(t, "a", content["answer_0"])
	assert.NotContains(t, content, "answer_1", "an undeclared question has no position")
}

// A question rejection reads `reason` off the decline, unlike the plan path.
func TestZCodeResolveControlResponse_QuestionRejectionDeclinesWithTheReason(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-q", ControlBehaviorDeny, "ask me later", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, ZCodeActionDecline, result[ZCodeReplyActionField])
	assert.Equal(t, "ask me later", result["reason"])
}

// The questions may arrive under `schema.questions` or hoisted to the top level. One
// reader serves both, so a build that moves them does not silently produce an
// unanswerable prompt.
func TestZCodeUserInputRequest_QuestionsAreReadFromEitherSpelling(t *testing.T) {
	t.Parallel()

	var nested zcodeUserInputRequest
	require.NoError(t, json.Unmarshal([]byte(`{"schema":{"questions":[{"question":"a"}]}}`), &nested))
	require.Len(t, nested.questions(), 1)
	assert.Equal(t, "a", nested.questions()[0].Question)

	var hoisted zcodeUserInputRequest
	require.NoError(t, json.Unmarshal([]byte(`{"questions":[{"question":"b"}]}`), &hoisted))
	require.Len(t, hoisted.questions(), 1)
	assert.Equal(t, "b", hoisted.questions()[0].Question)

	var both zcodeUserInputRequest
	require.NoError(t, json.Unmarshal(
		[]byte(`{"questions":[{"question":"top"}],"schema":{"questions":[{"question":"schema"}]}}`), &both))
	assert.Equal(t, "schema", both.questions()[0].Question, "the declared location wins")
}

// A question with an EMPTY option list is still a question: the shared control renders
// a free-text answer for it, and the reply must still be addressable.
func TestHandleUserInputRequest_AQuestionWithNoOptionsIsStillAnswerable(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput,
		`{"requestId":"req-q","schema":{"questions":[{"question":"Name it?","options":[]}]}}`))

	stored := zcodeStoredPayload(t, sink)
	frame, _ := zcodeResolve(t, stored, zcodeAnswer(t, "req-q", ControlBehaviorAllow, "",
		map[string]string{"Name it?": "leapmux"}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	content, ok := result[ZCodeReplyContentField].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "leapmux", content["answer_0"])
}

func TestHandleUserInputRequest_NoRequestIDDeclinesImmediately(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)

	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, `{"schema":{"questions":[]}}`))

	assert.Empty(t, sink.PersistedControls())
	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, ZCodeActionDecline, result[ZCodeReplyActionField])
}

// --- the requests LeapMux answers by itself ---

// The app-server BLOCKS behind these, so an unanswered one stalls the flow. The
// answer is honest about what LeapMux holds rather than a claim it cannot back.
func TestAnswerProviderRuntimeHeaders_ReportsAuthorizedOnlyWithAnInlineKey(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		providerID string
		authorized bool
	}{
		"a configured provider": {"builtin:zai", true},
		"an unknown provider":   {"nope", false},
		// A request that gives no provider asks about ANY of them, so a single
		// configured key is a truthful yes.
		"no provider at all": {"", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

			a.HandleOutput(zcodeRequestLine(t, 13, ZCodeMethodRequestProviderRuntimeHeaders,
				`{"providerId":"`+tc.providerID+`"}`))

			requests := stdin.Requests(t)
			require.Len(t, requests, 1)
			assert.Equal(t, "13", string(requests[0].ID))
			var result map[string]any
			require.NoError(t, json.Unmarshal(requests[0].Result, &result))
			assert.Equal(t, tc.authorized, result["authorized"])
			assert.Equal(t, map[string]any{}, result["headers"],
				"LeapMux mints no header: the key it holds is already in the registry")
			if tc.authorized {
				assert.NotContains(t, result, "error")
			} else {
				assert.Contains(t, result, "error", "an unauthorized answer must say why, or the retry is blind")
			}
		})
	}
}

// A malformed params object still gets an answer, because an unanswered request
// blocks the app-server. The answer is an honest UNAUTHORIZED even when the catalog
// holds keys: an undecodable request leaves the provider id empty, and an empty id
// asks about ANY provider -- so falling through would claim an authorization for a
// provider LeapMux holds no credential for, and the turn would fail later with an
// authentication error from the model provider instead of here.
func TestAnswerProviderRuntimeHeaders_MalformedParamsAreNeverAuthorized(t *testing.T) {
	t.Parallel()

	// `{"providerId":null}` is deliberately NOT here: it decodes cleanly to an empty id,
	// which is the legitimate "asks about ANY provider" request the sibling test covers.
	for name, params := range map[string]string{
		"a scalar":              `7`,
		"absent":                ``,
		"a numeric provider id": `{"providerId":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
			// A catalog that DOES hold keys, which is what makes the fall-through wrong.
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

			line := `{"id":13,"method":"interaction/requestProviderRuntimeHeaders"}`
			if params != "" {
				line = `{"id":13,"method":"interaction/requestProviderRuntimeHeaders","params":` + params + `}`
			}
			a.HandleOutput([]byte(line))

			requests := stdin.Requests(t)
			require.Len(t, requests, 1)
			assert.Equal(t, "13", string(requests[0].ID), "the answer must address the request that blocked")
			var result map[string]any
			require.NoError(t, json.Unmarshal(requests[0].Result, &result))
			assert.Equal(t, false, result["authorized"])
			assert.Contains(t, result, "error", "an unauthorized answer must say why, or the retry is blind")
		})
	}
}

func TestAnswerOfficialMcpAuthHeaders_DeclaresUnavailable(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)

	a.HandleOutput(zcodeRequestLine(t, 15, ZCodeMethodRequestOfficialMcpAuthHeaders, `{"server":"docs"}`))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, ZCodeOfficialAuthUnavailable, result["status"],
		"a declared unavailability lets the app-server fall through to the servers it can reach")
}

// An unknown server request is answered with method-not-found rather than ignored,
// for the same reason: the app-server is blocked until something comes back.
func TestHandleZCodeServerRequest_UnknownMethodAnswersMethodNotFound(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)

	a.HandleOutput(zcodeRequestLine(t, 17, "interaction/requestSomethingNew", `{}`))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.NotNil(t, requests[0].Error)
	assert.Equal(t, ZCodeErrMethodNotFound, requests[0].Error.Code)
}

// --- the answer paths that must NOT forward a frame ---

// Withholding the forward is the safe answer for every unusable answer: the
// app-server keeps waiting and the user can answer again, rather than receiving a
// frame that means nothing.
func TestZCodeResolveControlResponse_WithholdsAnUnusableForward(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	var withWireID zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(stored, &withWireID))
	withWireID.WireID = nil
	noWireID, err := json.Marshal(withWireID)
	require.NoError(t, err)

	cases := map[string]struct {
		payload []byte
		answer  []byte
	}{
		"no stored request at all": {
			nil, zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil),
		},
		"the stored request is not ours": {
			[]byte(`{"nope":`), zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil),
		},
		"the answer is not an allow or a deny": {
			stored, []byte(`{"type":"control_response","request_id":"req-1","response":{}}`),
		},
		"the answer addresses another request": {
			stored, zcodeAnswer(t, "req-OTHER", ControlBehaviorAllow, "", nil),
		},
		"the stored request has no wire id": {
			noWireID, zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil),
		},
		"the stored request has no reply shape": {
			[]byte(`{"type":"control_request","request_id":"req-1","id":7,"method":"unknown/method"}`),
			zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := zcodeProvider{}.ResolveControlResponse(ControlResponseContext{
				RequestPayload:  tc.payload,
				ResponseContent: tc.answer,
			})
			// Withhold, not an empty Content: buildControlResponsePlan backfills the raw
			// frontend envelope over an empty one, so clearing Content would forward the
			// exact frame these paths refuse to send.
			assert.True(t, res.Withhold, "no frame may reach the app-server's stdin")
		})
	}
}

// --- the provider plugin ---

func TestZCodeProvider_ClassifyGroupsTheRepeatingNotifications(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}

	// Two tools' automatic denials must stay distinguishable, so the tool name is in
	// the key.
	bash := provider.Classify(json.RawMessage(
		`{"type":"permission.resolved","payload":{"toolName":"Bash","decision":"deny"}}`))
	assert.Equal(t, NotificationKindProviderScoped, bash.Kind)
	assert.Contains(t, bash.Key, "Bash")
	write := provider.Classify(json.RawMessage(
		`{"type":"permission.resolved","payload":{"toolName":"Write","decision":"deny"}}`))
	assert.NotEqual(t, bash.Key, write.Key)

	// The steering queue flaps, so only the latest state matters.
	queued := provider.Classify(json.RawMessage(`{"type":"` + contracts.ZCodeEventTurnSteerQueued + `"}`))
	drained := provider.Classify(json.RawMessage(`{"type":"` + contracts.ZCodeEventTurnSteerDrained + `"}`))
	assert.Equal(t, NotificationKindStatus, queued.Kind)
	assert.Equal(t, queued.Key, drained.Key, "both sides of the flap collapse onto one row")

	assert.Equal(t, NotificationClassification{}, provider.Classify(json.RawMessage(`{"type":"turn.started"}`)))
	assert.Equal(t, NotificationClassification{}, provider.Classify(json.RawMessage(`not json`)))
}

func TestZCodeProvider_MergeKeepsTheLatest(t *testing.T) {
	t.Parallel()

	merged, err := zcodeProvider{}.Merge(NotificationClassification{},
		json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":2}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"b":2}`, string(merged))
}

func TestZCodeProvider_IsInterruptRecognizesTheStopFrame(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.True(t, provider.IsInterrupt(`{"id":1,"method":"session/stop","params":{}}`))
	assert.False(t, provider.IsInterrupt(`{"id":1,"method":"session/send"}`))
	assert.False(t, provider.IsInterrupt(`not json`))
	assert.False(t, provider.IsInterrupt(``))
}

func TestZCodeProvider_PlanModeControl(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	// Exit, not Prompt: the app-server ASKED and is blocked until it is answered.
	assert.Equal(t, PlanModeControlExit, provider.PlanModeControl(contracts.ZCodeToolNameExitPlanMode))
	assert.Equal(t, PlanModeControlEnter, provider.PlanModeControl(contracts.ZCodeToolNameEnterPlanMode))
	assert.Equal(t, PlanModeControlNone, provider.PlanModeControl("Bash"))
	assert.Equal(t, PlanModeControlNone, provider.PlanModeControl(""))
}

// The permission mode a plan-mode transition lands on is ZCode's OWN word for it.
// Claude's `acceptEdits` is not a value `session/setMode` accepts, so a session told
// that word stays where it was while the settings bar claims otherwise.
func TestZCodeProvider_PlanModePermissionMode(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.Equal(t, contracts.ZCodeModePlan, provider.PlanModePermissionMode(PlanModeControlEnter))
	assert.Equal(t, contracts.ZCodeModeBuild, provider.PlanModePermissionMode(PlanModeControlExit))
	assert.Empty(t, provider.PlanModePermissionMode(PlanModeControlPrompt),
		"ZCode's plan approval is an Exit, never a server-side Prompt")
	assert.Empty(t, provider.PlanModePermissionMode(PlanModeControlNone))

	// Every mode this returns must be one the app-server's own enumeration carries, or
	// the next launch sends a mode session/setMode rejects.
	modes := map[string]bool{contracts.ZCodeModePlan: true, contracts.ZCodeModeBuild: true, contracts.ZCodeModeEdit: true, contracts.ZCodeModeYolo: true}
	for _, kind := range []PlanModeControlKind{PlanModeControlEnter, PlanModeControlExit} {
		assert.True(t, modes[provider.PlanModePermissionMode(kind)],
			"kind %v returned a mode ZCode does not have", kind)
	}
}

func TestZCodeProvider_TurnEndToolUsesReadsTheStatedCount(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}

	count, ok := provider.TurnEndToolUses([]byte(`{"type":"turn.completed","payload":{"toolCallCount":7}}`))
	assert.True(t, ok)
	assert.Equal(t, int32(7), count)

	// Zero is a real answer, not an absent one.
	count, ok = provider.TurnEndToolUses([]byte(`{"type":"turn.completed","payload":{"toolCallCount":0}}`))
	assert.True(t, ok)
	assert.Equal(t, int32(0), count)

	// Without the field the shared reader decides, so a turn end from another shape
	// still reports whatever it can.
	_, ok = provider.TurnEndToolUses([]byte(`{"type":"turn.completed","payload":{}}`))
	assert.False(t, ok)
	_, ok = provider.TurnEndToolUses([]byte(`{"type":"turn.failed","payload":{"toolCallCount":3}}`))
	assert.False(t, ok)
	_, ok = provider.TurnEndToolUses([]byte(`not json`))
	assert.False(t, ok)
}

// ZCode's handle is an opaque TOKEN from session/list, so the token rule applies and
// the workspace path is irrelevant to it. A path-based provider resolves its handle
// against the workspace instead; ZCode must NOT, because a token is not a file.
func TestZCodeProvider_ResolveResumeHandleTakesATokenNotAPath(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	require.NoError(t, resumeHandleErr(provider, "01JQ8Z4T5N6P7R8S9T0V1W2X3Y", "/tmp/work"))
	require.NoError(t, resumeHandleErr(provider, "", "/tmp/work"),
		"an empty handle means no resume, which is not an error")
	require.NoError(t, resumeHandleErr(provider, "01JQ8Z4T5N6P7R8S9T0V1W2X3Y", ""),
		"the workspace does not participate: a token is resolved by the app-server, not on disk")

	// The token rule refuses what would corrupt the frame or the stored row.
	assert.Error(t, resumeHandleErr(provider, "has\na newline", "/tmp/work"))
	assert.Error(t, resumeHandleErr(provider, "  padded  ", "/tmp/work"))
	assert.Error(t, resumeHandleErr(provider, strings.Repeat("x", 4096), "/tmp/work"))
}

// A PDF reaches the model as binary garbage or is dropped with no message at all.
// Both are worse than a refusal that says so.
func TestZCodeProvider_ValidateAttachmentRefusesWhatTheAppServerCannotCarry(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	require.NoError(t, provider.ValidateAttachment(classifiedAttachment{kind: attachmentKindText, filename: "a.txt"}))
	require.NoError(t, provider.ValidateAttachment(classifiedAttachment{kind: attachmentKindImage, filename: "a.png"}),
		"the image gate depends on the running model, so it is not enforced here")

	err := provider.ValidateAttachment(classifiedAttachment{kind: attachmentKindPDF, filename: "spec.pdf"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.pdf", "the message must name the file the user attached")

	err = provider.ValidateAttachment(classifiedAttachment{kind: attachmentKindBinary, filename: "a.bin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a.bin")
}

func TestZCodeProvider_TheFalseCapabilitiesAreDeliberate(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.False(t, provider.IsSelfDisplayingControlTool(contracts.ZCodeToolNameAskUserQuestion),
		"the app-server echoes no control answer, so the synthetic row is the only record")
	assert.False(t, provider.SupportsChildSteering(), "a ZCode subagent takes no further message")
	assert.False(t, provider.EndsSubagentTranscript(nil))
	assert.Equal(t, PlanApprovalOptions{}, provider.PlanApprovalOptions())
	assert.Empty(t, provider.SyntheticInterruptNotice())
	_, ok := provider.PermissionModeFromRawInput(`{"mode":"yolo"}`)
	assert.False(t, ok, "ZCode's mode changes ride session/setMode, never a raw stdin frame")
	assert.Equal(t, contracts.ZCodeDefaultMode, provider.DefaultPermissionMode())
}

func TestZCodeProvider_IsRegistered(t *testing.T) {
	t.Parallel()

	assert.IsType(t, zcodeProvider{}, ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE))
}

// The pruned context is what the frontend renders the ANSWER from, after the pending
// request row is deleted. It must carry the labels the answer refers to.
func TestZCodeControlRequestContext_KeepsTheLabelsTheAnswerRefersTo(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))

	_, res := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-1", ControlBehaviorAllow, "", nil))
	require.NotEmpty(t, res.RequestContext)

	var ctx struct {
		Method  string `json:"method"`
		Request struct {
			ToolName string `json:"tool_name"`
		} `json:"request"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(res.RequestContext, &ctx))
	assert.Equal(t, ZCodeMethodRequestPermission, ctx.Method)
	assert.Equal(t, "Bash", ctx.Request.ToolName)
	require.Len(t, ctx.Options, 3)
	assert.Equal(t, "Allow once", ctx.Options[0].Name)

	// The verbose parts are pruned: the whole input and the embedded option responses
	// are not needed to render an answer, and they can be large.
	assert.NotContains(t, string(res.RequestContext), "rm -rf build")
	assert.NotContains(t, string(res.RequestContext), "permissionUpdates")
}

func TestZCodeControlRequestContext_KeepsTheQuestionLabels(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	_, res := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-q", ControlBehaviorAllow, "", map[string]string{"Which database?": "Postgres"}))

	var ctx struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		} `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(res.RequestContext, &ctx))
	require.Len(t, ctx.Questions, 2)
	assert.Equal(t, "Which database?", ctx.Questions[0].Question)
	assert.Equal(t, "Storage", ctx.Questions[0].Header)
}

// --- re-announcement ---
//
// The app-server re-sends an unanswered interaction request on a timer that starts
// at one second and doubles to ten, with the SAME requestId and a FRESH wire id.
// Only the first announcement may reach the user: the frontend de-duplicates on the
// request id AND the payload, and the payload carries the wire id, so a repeat that
// got through would stack a second banner every second until the user answered.

// A re-announcement republishes the FIRST announcement's bytes, unchanged.
//
// Byte-identical bytes are what makes the repeat harmless AND recoverable: the frontend
// de-duplicates a pending prompt on (request id, payload), so a banner the user already
// holds is left alone, while one that is gone -- an answer that produced no reply, a
// window that reconnected -- comes back. Publishing freshly built bytes instead would
// stack a second banner, because the wire id is inside the payload and it differs on
// every repeat.
func TestHandlePermissionRequest_AReannouncementRepublishesTheFirstPayload(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 8, ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 9, ZCodeMethodRequestPermission, zcodePermissionParams))

	persisted := sink.PersistedControls()
	broadcast := sink.BroadcastControls()
	require.Len(t, persisted, 3)
	require.Len(t, broadcast, 3)
	for i := 1; i < len(persisted); i++ {
		assert.Equal(t, string(persisted[0].Payload), string(persisted[i].Payload),
			"a repeat must republish the stored bytes, or the frontend stacks a banner")
		assert.Equal(t, string(broadcast[0].Payload), string(broadcast[i].Payload))
	}

	// The FIRST wire id is the one every stored payload addresses, and it stays valid:
	// the app-server registers every announced id against the same pending request.
	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(persisted[2].Payload, &stored))
	assert.JSONEq(t, `7`, string(stored.WireID))
}

func TestHandlePermissionRequest_AResolvedRequestIDIsForwardedAgain(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"req-1","toolCallId":"call-1","toolName":"Bash","decision":"allow"}`))
	a.HandleOutput(zcodeRequestLine(t, 8, ZCodeMethodRequestPermission, zcodePermissionParams))

	assert.Len(t, sink.PersistedControls(), 2,
		"the guard is re-armed by the resolution, so a genuinely new prompt on the same id is shown")
}

// Same rule for a question or a plan approval. See the permission twin above.
func TestHandleUserInputRequest_AReannouncementRepublishesTheFirstPayload(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))
	a.HandleOutput(zcodeRequestLine(t, 12, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	persisted := sink.PersistedControls()
	require.Len(t, persisted, 2)
	assert.Equal(t, string(persisted[0].Payload), string(persisted[1].Payload))
	require.Len(t, sink.BroadcastControls(), 2)

	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(persisted[1].Payload, &stored))
	assert.JSONEq(t, `11`, string(stored.WireID))
}

func TestHandleUserInputRequest_UserInputResolvedReArmsTheGuard(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeRequestLine(t, 11, ZCodeMethodRequestUserInput, zcodeQuestionParams))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventUserInputResolved, `{"requestId":"req-q"}`))
	a.HandleOutput(zcodeRequestLine(t, 12, ZCodeMethodRequestUserInput, zcodeQuestionParams))

	assert.Len(t, sink.PersistedControls(), 2)
	assert.Empty(t, sink.PersistedNotifications(), "userInput.resolved renders nothing of its own")
}

// A request with no id cannot be de-duplicated, and it is answered rather than
// forwarded -- so the guard must not swallow the second one.
func TestHandlePermissionRequest_AnIDLessRequestIsAlwaysAnswered(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &recordingControlSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)

	params := `{"sessionId":"sess-1","toolName":"Bash","options":[{"optionId":"deny","kind":"deny","response":{"decision":"deny"}}]}`
	a.HandleOutput(zcodeRequestLine(t, 7, ZCodeMethodRequestPermission, params))
	a.HandleOutput(zcodeRequestLine(t, 8, ZCodeMethodRequestPermission, params))

	assert.Empty(t, sink.PersistedControls())
	assert.Len(t, stdin.Requests(t), 2, "each unroutable request gets its own denial")
}

// --- permission bookkeeping ---

func TestZCodePermissionBookkeeping_ForgetIsOneShot(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})

	assert.False(t, a.forgetZCodeControlRequest("req-1"), "an unknown id was never forwarded")
	assert.False(t, a.forgetZCodeControlRequest(""))

	a.rememberZCodeControlRequest("req-1", json.RawMessage(`{"type":"control_request"}`))
	assert.True(t, a.forgetZCodeControlRequest("req-1"))
	assert.False(t, a.forgetZCodeControlRequest("req-1"), "a second resolved must not read as forwarded again")
}

// The stored question ORDER is what makes the positional fallback line up with the
// app-server's own list, so it must be the declared order and not a map iteration.
func TestZCodeStoredQuestionTexts_PreservesTheDeclaredOrder(t *testing.T) {
	t.Parallel()

	texts := zcodeStoredQuestionTexts(json.RawMessage(
		`{"questions":[{"question":"zeta"},{"question":"alpha"},{"question":""}]}`))
	assert.Equal(t, []string{"zeta", "alpha", ""}, texts)

	assert.Nil(t, zcodeStoredQuestionTexts(nil))
	assert.Nil(t, zcodeStoredQuestionTexts(json.RawMessage(`not json`)))
	assert.Empty(t, zcodeStoredQuestionTexts(json.RawMessage(`{}`)))
}

func TestZCodeAnswersFromResponse(t *testing.T) {
	t.Parallel()

	answers := zcodeAnswersFromResponse(zcodeAnswer(t, "req-q", ControlBehaviorAllow, "",
		map[string]string{"q": "a"}))
	assert.Equal(t, map[string]string{"q": "a"}, answers)

	assert.Nil(t, zcodeAnswersFromResponse([]byte(`not json`)))
	assert.Nil(t, zcodeAnswersFromResponse(zcodeAnswer(t, "req-q", ControlBehaviorAllow, "", nil)))
}

// --- to-do list ---

func TestZCodeExtractTodoEvent_ReadsTheScheduledInputAsASnapshot(t *testing.T) {
	t.Parallel()

	content := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"c1",
	  "toolName":"TodoWrite","input":{"todos":[
	    {"content":"Write the parser","status":"in_progress","activeForm":"Writing the parser"},
	    {"content":"Add tests","status":"pending"},
	    {"content":"Read the spec","status":"completed"}]}}}`)

	ev, ok := zcodeProvider{}.ExtractTodoEvent(contracts.ZCodeToolNameTodoWrite, content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, ev.Kind,
		"ZCode re-sends the WHOLE list, so an incremental apply would keep a deleted row")
	require.Len(t, ev.Snapshot, 3)
	assert.Equal(t, "Write the parser", ev.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusInProgress, ev.Snapshot[0].Status)
	assert.Equal(t, "Writing the parser", ev.Snapshot[0].ActiveForm)
	assert.Equal(t, todoevents.StatusPending, ev.Snapshot[1].Status)
	assert.Equal(t, todoevents.StatusCompleted, ev.Snapshot[2].Status)
}

func TestZCodeExtractTodoEvent_IgnoresEverythingElse(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spanType string
		content  string
	}{
		"another tool": {contracts.ZCodeToolNameBash,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"Bash","input":{"command":"ls"}}}`},
		"the result half": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"result","toolName":"TodoWrite","result":{"success":true}}}`},
		"a batch summary": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"batch","toolCallIds":["c1"]}}`},
		"another event type": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"session.updated","payload":{"content":"done"}}`},
		"a span type the tool name contradicts": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"Bash","input":{"todos":[]}}}`},
		"not json":     {contracts.ZCodeToolNameTodoWrite, `not json`},
		"no span type": {"", `{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite","input":{"todos":[]}}}`},
		// An input that was never recovered must be read as "the list did not change",
		// never as an empty list -- a snapshot of zero rows DELETES every row, so the
		// user's checklist would vanish mid-turn. `inputOmitted` is the NORM: the
		// app-server sends no input of its own, and openZCodeToolCallInto substitutes the
		// model-stream cache, which is best effort (empty after a resume, after a context
		// clear, and when the stream was cut).
		"an input the recovery never filled in": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite",
			  "inputOmitted":true,"inputRef":"model_stream"}}`},
		"an input that is JSON null": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite","input":null}}`},
		"an input that carries no todos key": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite","input":{}}}`},
		"an input whose todos is null": {contracts.ZCodeToolNameTodoWrite,
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite","input":{"todos":null}}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := zcodeProvider{}.ExtractTodoEvent(tc.spanType, []byte(tc.content), nil)
			assert.False(t, ok)
		})
	}
}

// An emptied list is a real snapshot: the model deleted every row, and reporting
// nothing would leave the sidebar showing work that is done.
//
// The `todos` key is PRESENT here, and that is what tells a deliberate clear apart from
// an input the recovery never filled in. See the absent-input cases above.
func TestZCodeExtractTodoEvent_AnEmptyListIsASnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := zcodeProvider{}.ExtractTodoEvent(contracts.ZCodeToolNameTodoWrite,
		[]byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolName":"TodoWrite","input":{"todos":[]}}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, ev.Kind)
	assert.Empty(t, ev.Snapshot)
}
