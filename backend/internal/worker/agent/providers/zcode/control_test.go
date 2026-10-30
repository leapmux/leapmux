package zcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodePlanApprovalWithoutToolRowPersistsOriginal(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	raw := []byte(` {"id":"server-1","method":"interaction/requestUserInput","params":{"requestId":"plan","toolCallId":"missing-call","toolName":"ExitPlanMode","input":{"plan":"# Stored plan"},"schema":{"interaction":"plan_approval"},"future":9007199254740993}} `)
	a.HandleOutput(raw)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[0].Source)
	assert.True(t, messages[0].NoSpan)
	a.HandleOutput(bytes.Replace(raw, []byte(`"server-1"`), []byte(`"server-2"`), 1))
	assert.Len(t, sink.Messages(), 1)
}

func TestZCodeConcurrentPlanAnnouncementsPersistOneSource(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	const count = 12
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			a.HandleOutput([]byte(`{"id":` + strconv.Itoa(index) + `,"method":"interaction/requestUserInput","params":{"requestId":"plan","toolCallId":"missing-call","input":{"plan":"# One plan"},"schema":{"interaction":"plan_approval"}}}`))
		}()
	}
	close(start)
	workers.Wait()
	require.Len(t, sink.Messages(), 1)
	requests := sink.PublishedControls()
	require.Len(t, requests, count)
	for _, request := range requests {
		assert.Equal(t, requests[0].Payload, request.Payload)
	}
}

func TestZCodePlanStorageFailureDoesNotPublishApproval(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	sink.PersistErr = errors.New("storage unavailable")
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput([]byte(`{"id":"server-1","method":"interaction/requestUserInput","params":{"requestId":"plan","input":{"plan":"# One plan"},"schema":{"interaction":"plan_approval"}}}`))
	assert.Empty(t, sink.PublishedControls())
	frames := stdin.Frames()
	require.Len(t, frames, 1)
	var response zcodeSentRequest
	require.NoError(t, json.Unmarshal(frames[0], &response))
	require.NotNil(t, response.Error)
	assert.Equal(t, ErrInternal, response.Error.Code)
}

func TestZCodeControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.ZCodeMethodRequestPermission, contracts.ZCodeMethodRequestUserInput} {
		t.Run(method, func(t *testing.T) {
			sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			a.HandleOutput(zcodeRequestLine(t, 27, method, `{"requestId":"control-1","toolName":"Read","schema":{"questions":[]}}`))
			frames := stdin.Frames()
			require.Len(t, frames, 1)
			var response zcodeSentRequest
			require.NoError(t, json.Unmarshal(frames[0], &response))
			assert.Equal(t, "27", string(response.ID))
			require.NotNil(t, response.Error)
			assert.Equal(t, ErrInternal, response.Error.Code)
			assert.Equal(t, providerkit.ControlPublicationFailure, response.Error.Message)
			assert.Empty(t, sink.PublishedControls())
			assert.Nil(t, a.pendingZCodeControlPayload("control-1"))
		})
	}
}

func TestZCodePlanControlUsesNativeInputPlan(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestUserInput, `{
		"requestId":"plan", "toolCallId":"plan-call", "toolName":"ExitPlanMode",
		"prompt":"Tool ExitPlanMode requires user interaction",
		"input":{"plan":"# Welcome plan"},
		"schema":{"interaction":"plan_approval","toolName":"ExitPlanMode"}
	}`))
	var stored struct {
		Request struct {
			Input struct {
				Plan string
			}
		}
	}
	require.NoError(t, json.Unmarshal(zcodeStoredPayload(t, sink), &stored))
	assert.Equal(t, "# Welcome plan", stored.Request.Input.Plan)
}

func TestZCodeControlInputSupplementsThePersistedToolRequest(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	original := []byte(` {"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"plan-call","toolName":"ExitPlanMode","inputOmitted":true,"future":9007199254740993}} `)
	event, ok := parseZCodeEvent(original)
	require.True(t, ok)
	a.dispatchZCodeEventOrdered(event)
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestUserInput, `{
		"requestId":"plan", "toolCallId":"plan-call", "toolName":"ExitPlanMode",
		"input":{"plan":"# Welcome plan"}, "schema":{"interaction":"plan_approval"}
	}`))
	message := sink.Messages()[0]
	assert.Equal(t, original, message.Content)
	assert.Contains(t, string(message.SupplementalContent), "# Welcome plan")
	assert.Equal(t, int64(1), message.SupplementalRevision)
	resolved := agent.ResolveMessageContent(zcodeProvider{}, agent.MessageContent{Original: message.Content, Supplemental: message.SupplementalContent})
	assert.Contains(t, string(resolved), `"plan":"# Welcome plan"`)
	assert.Contains(t, string(resolved), `9007199254740993`)
	a.HandleOutput(zcodeRequestLine(t, 8, contracts.ZCodeMethodRequestUserInput, `{
		"requestId":"plan", "toolCallId":"plan-call", "toolName":"ExitPlanMode",
		"input":{"plan":"# Welcome plan"}, "schema":{"interaction":"plan_approval"}
	}`))
	assert.Equal(t, int64(1), sink.Messages()[0].SupplementalRevision)
}

func TestZCodePlanControlSupplementsPartialInput(t *testing.T) {
	t.Parallel()
	for _, controlInput := range []string{`"input":{"plan":"# Welcome plan"}`, `"input":{"allowedPrompts":[]},"context":{"plan":"# Welcome plan"}`} {
		t.Run(controlInput, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
			original := []byte(` {"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"plan-call","toolName":"ExitPlanMode","input":{"allowedPrompts":[],"future":9007199254740993}}} `)
			event, ok := parseZCodeEvent(original)
			require.True(t, ok)
			a.dispatchZCodeEventOrdered(event)
			a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestUserInput, `{"requestId":"plan","toolCallId":"plan-call","toolName":"ExitPlanMode","schema":{"interaction":"plan_approval"},`+controlInput+`}`))
			messages := sink.Messages()
			require.Len(t, messages, 1)
			assert.Equal(t, original, messages[0].Content)
			resolved := agent.ResolveMessageContent(zcodeProvider{}, agent.MessageContent{Original: messages[0].Content, Supplemental: messages[0].SupplementalContent})
			assert.Contains(t, string(resolved), `"plan":"# Welcome plan"`)
			assert.Contains(t, string(resolved), `"allowedPrompts":[]`)
			assert.Contains(t, string(resolved), `9007199254740993`)
		})
	}
}

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
func zcodeStoredPayload(t *testing.T, sink *agenttest.ControlSink) []byte {
	t.Helper()
	persisted := sink.PublishedControls()
	require.Len(t, persisted, 1, "a control request must be persisted exactly once")
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
	return zcodeMarshalAnswer(t, requestID, inner)
}

func zcodeAnswerWithChoice(t *testing.T, requestID, behavior, message, choice string) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	if message != "" {
		inner["message"] = message
	}
	if choice != "" {
		inner["choice"] = choice
	}
	return zcodeMarshalAnswer(t, requestID, inner)
}

func zcodeMarshalAnswer(t *testing.T, requestID string, inner map[string]any) []byte {
	t.Helper()
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
func zcodeResolve(t *testing.T, stored, answer []byte) (zcodeDecodedReply, agent.ControlResponseResolution) {
	t.Helper()
	res := zcodeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	zcodeStartTestTurn(t, a, `{"turnNumber":1}`)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","toolCallId":"call-1","toolName":"Bash","inputOmitted":true}`))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))

	payload := zcodeStoredPayload(t, sink)
	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(payload, &stored))
	assert.Equal(t, "control_request", stored.Type)
	assert.Equal(t, "req-1", stored.RequestID)
	assert.Equal(t, contracts.ZCodeMethodRequestPermission, stored.Method)
	assert.Equal(t, "Bash", stored.Request.ToolName)
	assert.Equal(t, zcodeTestSpanID(t, sink, "call-1"), stored.Request.ToolUseID)
	assert.JSONEq(t, `{"command":"rm -rf build"}`, string(stored.Request.Input))
	assert.JSONEq(t, zcodePermissionParams, string(stored.Params),
		"ZCode's own params must survive verbatim, because the answer path reads the option list off them")

	assert.Equal(t, "7", string(stored.WireID))
	extracted, _, ok := agent.ExtractJSONRPCID(payload)
	require.True(t, ok, "the wire id must be spelled `id`, or the shared extractor cannot find it")
	assert.Equal(t, "7", string(extracted))

	// Nothing is answered yet: the whole point of a prompt is that the app-server waits.
	assert.Empty(t, a.StdinForTest().(*zcodeRecordedStdin).Frames())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.NotEmpty(t, a.pendingControls["req-1"],
		"the prompt's payload is remembered so a re-announcement republishes THESE bytes, and so its "+
			"permission.resolved echo is not read as an automatic decision")
}

// An allow echoes the CHOSEN option's own response, because it carries the
// permission-rule updates an "always allow" implies. Rebuilding a bare decision
// would drop them and re-prompt on the next identical call.
func TestZCodeResolveControlResponse_AllowEchoesTheOptionResponse(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	frame, res := zcodeResolve(t, stored, zcodeAnswer(t, "req-1", agent.ControlBehaviorAllow, "", nil))
	assert.Equal(t, "7", string(frame.ID))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeDecisionAllow, result["decision"])
	assert.Equal(t, "once", result["scope"], "the first allowing option's response is echoed whole")
	assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
}

// The answer's CHOICE names the option the reader picked in the pill group. That
// option's response is the exact native reply -- "Always allow in this project"
// carries permissionUpdates a bare allow would drop, and picking it must echo ITS
// response even though an earlier option also allows.
func TestZCodeResolveControlResponse_AChosenOptionIdEchoesThatOptionResponse(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	frame, _ := zcodeResolve(t, stored, zcodeAnswerWithChoice(t, "req-1", agent.ControlBehaviorAllow, "", "allow_always"))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeDecisionAllow, result["decision"])
	assert.NotContains(t, result, "scope", "the chosen always option's response is echoed whole, not the once option's")
	assert.Equal(t, []any{map[string]any{"rule": "Bash(rm:*)"}},
		result["permissionUpdates"], "the chosen always option's rule updates reach the backend")

	// A choice that names no offered option changes nothing: the decision word
	// still picks the first allowing option, so a stale pill cannot invent a grant.
	frame, _ = zcodeResolve(t, stored, zcodeAnswerWithChoice(t, "req-1", agent.ControlBehaviorAllow, "", "allow_project"))
	var fallback map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &fallback))
	assert.Equal(t, "once", fallback["scope"], "an unknown choice id falls back to the decision word")
	assert.NotContains(t, fallback, "permissionUpdates")
}

func TestZCodeResolveControlResponse_DenyCarriesTheUsersReason(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	stored := zcodeStoredPayload(t, sink)

	frame, _ := zcodeResolve(t, stored, zcodeAnswer(t, "req-1", agent.ControlBehaviorDeny, "not on my machine", nil))
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
	raw, err := zcodePermissionResult(options, agent.ControlBehaviorAllow, "", "")
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

	for _, behavior := range []string{agent.ControlBehaviorAllow, agent.ControlBehaviorDeny} {
		raw, err := zcodePermissionResult(nil, behavior, "because", "")
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.Unmarshal(raw, &result))
		want := contracts.ZCodeDecisionAllow
		if behavior != agent.ControlBehaviorAllow {
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

	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission,
		`{"toolName":"Bash","options":[{"optionId":"deny","response":{"decision":"deny"}}]}`))

	assert.Empty(t, sink.PublishedControls(), "an unanswerable prompt must not become a pending row")
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

	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	a.HandleOutput([]byte(`{"id":7,"method":"interaction/requestPermission","params":"not an object"}`))

	assert.Empty(t, sink.PublishedControls())
	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.NotNil(t, requests[0].Error)
	assert.Equal(t, ErrInternal, requests[0].Error.Code)
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

func TestZCodePlanApprovalStoresThePlanOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))
	require.Equal(t, 1, sink.PlanUpdateCount())
	assert.Equal(t, "1. read\n2. write", string(sink.LastPlanUpdate().Content))
	assert.Equal(t, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, sink.LastPlanUpdate().Compression)
	a.HandleOutput(zcodeRequestLine(t, 10, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))
	assert.Equal(t, 1, sink.PlanUpdateCount(), "a repeated request keeps the same stored plan")
}

func TestHandleUserInputRequest_PlanApprovalIsStoredAsAPlanControl(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))

	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(zcodeStoredPayload(t, sink), &stored))
	assert.Equal(t, contracts.ZCodeToolNameExitPlanMode, stored.Request.ToolName,
		"the plan surface is selected by tool name, so it must be ZCode's plan tool")
	assert.Equal(t, agent.PlanModeControlExit, zcodeProvider{}.PlanModeControl(stored.Request.ToolName))

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
	assert.Equal(t, contracts.ZCodePlanControlQuestion, input.Questions[0].Question)
	require.Len(t, input.Questions[0].Options, 1)
	assert.Equal(t, contracts.ZCodePlanControlApprove, input.Questions[0].Options[0].Value)

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
	// claude.Agent.detectPlanModeFromToolResult, which no ZCode path reaches -- and
	// the shared code deletes an entry only on an ALLOW, so a denied or abandoned plan
	// left one in a process-global map for the worker's life.
	_, ok := sink.LoadAndDeletePlanModeToolUse("call-plan")
	assert.False(t, ok, "nothing reads it for ZCode, and only an allow would ever remove it")
}

// The sentinel is the ONLY value the app-server's plan reader accepts as approval.
func TestZCodeResolveControlResponse_PlanAcceptSendsTheSentinel(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))

	frame, res := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", agent.ControlBehaviorAllow, "", nil))
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeActionAccept, result[contracts.ZCodeReplyFieldAction])
	content, ok := result[contracts.ZCodeReplyFieldContent].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, contracts.ZCodePlanControlApprove, content[contracts.ZCodeAnswerFieldSingle])
}

// Native rejection stays separate from feedback because an accepted plan answer can approve the plan.
func TestZCodeResolveControlResponse_PlanRejectionWithFeedbackKeepsTheText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))
	frame, resolution := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", agent.ControlBehaviorDeny, "  split step 2  ", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeActionDecline, result[contracts.ZCodeReplyFieldAction])
	assert.NotContains(t, result, contracts.ZCodeReplyFieldContent)
	// The feedback is the reader's own typed text, and it reaches the agent as the next
	// user input, so it keeps every byte the reader typed.
	assert.Equal(t, agent.FeedbackFollowUp("  split step 2  "), resolution.FollowUp)
}

// With nothing typed there is no text to preserve, so the honest reply is a decline.
func TestZCodeResolveControlResponse_PlanRejectionWithNoFeedbackDeclines(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, zcodePlanParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-plan", agent.ControlBehaviorDeny, "   ", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeActionDecline, result[contracts.ZCodeReplyFieldAction])
	assert.NotContains(t, result, contracts.ZCodeReplyFieldContent)
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))

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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))

	frame, _ := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-q", agent.ControlBehaviorAllow, "", map[string]string{
			"Which database?": "Postgres",
			"Which extras?":   "cache, Metrics",
		}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeActionAccept, result[contracts.ZCodeReplyFieldAction])
	content, ok := result[contracts.ZCodeReplyFieldContent].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "Postgres", content["answer_0"])
	assert.Equal(t, "cache, Metrics", content["answer_1"])
	answers, ok := content[contracts.ZCodeAnswerFieldMap].(map[string]any)
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
	answers, ok := content[contracts.ZCodeAnswerFieldMap].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"first": "yes"}, answers)
}

// An EMPTY answers object is not the same as an absent one: the reader short-circuits
// to "answered with nothing" and suppresses the positional fallback.
func TestZCodeAnswerContent_OmitsTheAnswersMapEntirelyWhenEmpty(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{Questions: []string{"only"}})
	assert.NotContains(t, content, contracts.ZCodeAnswerFieldMap)
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
	assert.NotContains(t, content, contracts.ZCodeAnswerFieldMap)
}

// An answer whose question the request never declared cannot be placed positionally,
// but its key can still match -- which beats dropping what the user chose.
func TestZCodeAnswerContent_AnUndeclaredQuestionKeepsItsKeyedAnswer(t *testing.T) {
	t.Parallel()

	content := zcodeAnswerContent(zcodeUserInputReply{
		Questions: []string{"declared"},
		Answers:   map[string]string{"declared": "a", "surprise": "b"},
	})
	answers, ok := content[contracts.ZCodeAnswerFieldMap].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"declared": "a", "surprise": "b"}, answers)
	assert.Equal(t, "a", content["answer_0"])
	assert.NotContains(t, content, "answer_1", "an undeclared question has no position")
}

// The native question mapper discards a rejection reason, so delivery uses a separate input.
func TestZCodeResolveControlResponse_QuestionRejectionKeepsSeparateFeedback(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))
	frame, resolution := zcodeResolve(t, zcodeStoredPayload(t, sink),
		zcodeAnswer(t, "req-q", agent.ControlBehaviorDeny, "ask me later", nil))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	assert.Equal(t, contracts.ZCodeActionDecline, result[contracts.ZCodeReplyFieldAction])
	assert.NotContains(t, result, "reason")
	assert.Equal(t, agent.FeedbackFollowUp("ask me later"), resolution.FollowUp)
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput,
		`{"requestId":"req-q","schema":{"questions":[{"question":"Name it?","options":[]}]}}`))

	stored := zcodeStoredPayload(t, sink)
	frame, _ := zcodeResolve(t, stored, zcodeAnswer(t, "req-q", agent.ControlBehaviorAllow, "",
		map[string]string{"Name it?": "leapmux"}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	content, ok := result[contracts.ZCodeReplyFieldContent].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "leapmux", content["answer_0"])
}

func TestHandleUserInputRequest_NoRequestIDDeclinesImmediately(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, `{"schema":{"questions":[]}}`))

	assert.Empty(t, sink.PublishedControls())
	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, contracts.ZCodeActionDecline, result[contracts.ZCodeReplyFieldAction])
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
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

			a.HandleOutput(zcodeRequestLine(t, 13, MethodRequestProviderRuntimeHeaders,
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

func TestAnswerProviderRuntimeHeaders_AccountConfigReturnsRequestAuth(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
	    }`)
	seedZCodeAccountStateForTests(t, a)

	a.HandleOutput(zcodeRequestLine(t, 13, MethodRequestProviderRuntimeHeaders,
		`{"providerId":"account:zai-individual-coding-plan"}`))

	requests := zcodeAwaitReplies(t, stdin, 1)
	var result struct {
		HeadersApplied bool `json:"headersApplied"`
		RequestAuth    struct {
			APIKey string `json:"apiKey"`
		} `json:"requestAuth"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.True(t, result.HeadersApplied)
	assert.Equal(t, "plan-key", result.RequestAuth.APIKey)
}

func TestAnswerProviderRuntimeHeaders_AccountConfigReportsAMissingCredential(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
    }`)
	a.Mu.Lock()
	a.account = &zcodeAccountState{}
	a.Mu.Unlock()

	a.HandleOutput(zcodeRequestLine(t, 14, MethodRequestProviderRuntimeHeaders,
		`{"providerId":"account:bigmodel-individual-coding-plan"}`))

	requests := zcodeAwaitReplies(t, stdin, 1)
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	var applied bool
	require.NoError(t, json.Unmarshal(result["headersApplied"], &applied))
	assert.False(t, applied)
	assert.NotContains(t, result, "requestAuth")
	var message string
	require.NoError(t, json.Unmarshal(result["errorMessage"], &message))
	assert.Contains(t, message, "account:bigmodel-individual-coding-plan")
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
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
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

// --- runtime-header answers: cancellation by the LOGICAL request id ---
//
// One interaction/requestProviderRuntimeHeaders carries TWO identities. The
// JSON-RPC envelope id is only the reply address. The logical `params.requestId`
// (`<session>:provider-runtime-headers:<uuid>`) is the key a cancellation
// specifies. The fixtures below keep the two deliberately different.

const zcodeTestRuntimeWorkspace = `{"workspacePath":"/tmp/zcode-workspace","workspaceKey":"/tmp/zcode-workspace"}`
const zcodeTestOtherWorkspace = `{"workspacePath":"/tmp/zcode-other","workspaceKey":"/tmp/zcode-other"}`

// zcodeRuntimeHeadersLine renders one interaction/requestProviderRuntimeHeaders
// request: envelopeID is the reply address, requestID the logical id.
func zcodeRuntimeHeadersLine(t *testing.T, envelopeID, requestID, sessionID, workspace, providerID string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id":     envelopeID,
		"method": MethodRequestProviderRuntimeHeaders,
		"params": map[string]any{
			"requestId":  requestID,
			"sessionId":  sessionID,
			"workspace":  json.RawMessage(workspace),
			"providerId": providerID,
			"reason":     "model-request",
		},
	})
	require.NoError(t, err)
	return line
}

// zcodeRuntimeHeadersCancelLine renders one providerRuntimeHeadersCancelled
// notification: the app-server withdrawing one request.
func zcodeRuntimeHeadersCancelLine(t *testing.T, requestID, sessionID, workspace string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"method": NotifyProviderRuntimeHeadersCancelled,
		"params": map[string]any{
			"requestId": requestID,
			"sessionId": sessionID,
			"workspace": json.RawMessage(workspace),
		},
	})
	require.NoError(t, err)
	return line
}

// zcodeBlockedStdin holds the FIRST write to the agent's stdin until the test
// releases it, so a test can gate between the completion of an answer and its
// reply. Later writes pass through. Every record lands in the wrapped recorder.
type zcodeBlockedStdin struct {
	rec     *zcodeRecordedStdin
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func zcodeBlockFirstStdin(rec *zcodeRecordedStdin) *zcodeBlockedStdin {
	return &zcodeBlockedStdin{rec: rec, started: make(chan struct{}), release: make(chan struct{})}
}

func (w *zcodeBlockedStdin) Write(p []byte) (int, error) {
	first := false
	w.once.Do(func() { first = true })
	if first {
		close(w.started)
		<-w.release
	}
	return w.rec.Write(p)
}

func (w *zcodeBlockedStdin) Close() error { return w.rec.Close() }

// The envelope id is the reply address alone: a cancellation that specifies it
// cancels nothing. The logical id is the cancellation key.
func TestAnswerProviderRuntimeHeaders_CancelsByLogicalIDAndNotByEnvelopeID(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	const logical = "sess-1:provider-runtime-headers:0d4c7c3e-5a3f-4a1e-9f0d-1b2c3d4e5f60"

	handler, consumed := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	require.True(t, consumed)
	require.NotNil(t, handler)
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, "server-1", "sess-1", zcodeTestRuntimeWorkspace))
	handler()
	replies := stdin.Requests(t)
	require.Len(t, replies, 1, "the envelope id is not a cancellation key")
	assert.Equal(t, `"server-1"`, string(replies[0].ID))

	handler2, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-2", logical+"-2", "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical+"-2", "sess-1", zcodeTestRuntimeWorkspace))
	handler2()
	assert.Len(t, stdin.Requests(t), 1, "the app-server withdrew the request: the answer must not arrive")
}

// The handler runs on a goroutine of its own after the registration. A
// cancellation that lands before the handler runs must already abort the
// credential work: no request reaches the network and no reply is written.
func TestAnswerProviderRuntimeHeaders_CancellationBeforeTheHandlerSuppressesTheReply(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	const logical = "sess-1:provider-runtime-headers:6f1e2d3c-8b4a-4c5d-9e7f-0a1b2c3d4e5f"

	handler, consumed := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan")))
	require.True(t, consumed)
	require.NotNil(t, handler)
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	handler()
	require.Never(t, func() bool { return len(api.recorded()) > 0 }, zcodeCancelPromptness, time.Millisecond,
		"a cancelled answer sends no credential request")
	assert.Empty(t, a.StdinForTest().(*zcodeRecordedStdin).Frames(),
		"the app-server withdrew the request: the answer must not arrive")
}

// Completion commits to the reply BEFORE the write. A cancellation at that
// gate returns without waiting for the blocked write and does not withdraw
// the reply that already won.
func TestAnswerProviderRuntimeHeaders_TheReplyAlreadyWonWhileTheWriteBlocks(t *testing.T) {
	t.Parallel()

	a, _ := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	rec := a.StdinForTest().(*zcodeRecordedStdin)
	blocked := zcodeBlockFirstStdin(rec)
	a.SetStdinForTest(blocked)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	t.Cleanup(unblock)
	const logical = "sess-1:provider-runtime-headers:11111111-2222-4333-8444-555555555555"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	<-blocked.started

	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	}()
	select {
	case <-cancelled:
	case <-time.After(30 * time.Second):
		t.Fatal("the cancellation waited for the blocked reply write")
	}

	unblock()
	replies := zcodeAwaitReplies(t, rec, 1)
	assert.Equal(t, `"server-1"`, string(replies[0].ID), "the reply already won the state transition")
}

// The state half of the gate: completion commits the answer to `replying`
// BEFORE the write and holds its registration THROUGH the write.
func TestAnswerProviderRuntimeHeaders_TheGateCommitsBeforeTheWrite(t *testing.T) {
	t.Parallel()

	a, _ := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	rec := a.StdinForTest().(*zcodeRecordedStdin)
	blocked := zcodeBlockFirstStdin(rec)
	a.SetStdinForTest(blocked)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	t.Cleanup(unblock)
	const logical = "sess-1:provider-runtime-headers:aaaa1111-2222-4333-8444-555555555555"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	<-blocked.started

	a.runtimeHeaderMu.Lock()
	answer := a.runtimeHeaderAnswers[logical]
	a.runtimeHeaderMu.Unlock()
	require.NotNil(t, answer, "the answer is registered under its logical id")
	assert.Equal(t, zcodeRuntimeHeaderReplying, answer.state,
		"completion commits to the reply before the write starts")
	assert.Equal(t, `"server-1"`, string(answer.envelopeID))

	unblock()
	_ = zcodeAwaitReplies(t, rec, 1)
	a.runtimeHeaderMu.Lock()
	remaining := len(a.runtimeHeaderAnswers)
	a.runtimeHeaderMu.Unlock()
	assert.Zero(t, remaining, "completion removes its own registration after the write")
}

// A duplicate logical request id must not replace the existing registration:
// the cancellation then cancels the FIRST answer only, and the duplicate keeps
// its own reply.
func TestAnswerProviderRuntimeHeaders_DuplicateLogicalIDDoesNotReplaceTheRegistration(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	const logical = "sess-1:provider-runtime-headers:22222222-3333-4444-8555-666666666666"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-2", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	require.Eventually(t, func() bool { return api.mintStarted.Load() >= 1 }, 30*time.Second, time.Millisecond)

	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	// The cancelled waiter leaves the shared mint; the duplicate's waiter keeps
	// it alive.
	require.Eventually(t, func() bool { return a.projectTokens.mints.waitingInAll() == 1 },
		zcodeCancelPromptness, time.Millisecond,
		"the cancelled waiter must leave the shared call for the duplicate")
	unblock()

	stdin := a.StdinForTest().(*zcodeRecordedStdin)
	replies := zcodeAwaitReplies(t, stdin, 1)
	assert.Equal(t, `"server-2"`, string(replies[0].ID),
		"the cancellation cancelled the first answer; the duplicate keeps its own reply")
	assert.Len(t, stdin.Frames(), 1, "the cancelled first answer writes nothing")
}

// A cancelled answer that completes late must not remove or cancel a newer
// answer that reused its logical request id, and its own entry must not linger
// to shadow that reuse.
func TestAnswerProviderRuntimeHeaders_RequestIDReuseKeepsTheNewerAnswer(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	const logical = "sess-1:provider-runtime-headers:33333333-4444-4555-8666-777777777777"

	// The first answer is cancelled before its handler runs, so its completion
	// arrives late -- after the second answer took the id.
	first, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	second, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-2", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	first()
	assert.Empty(t, stdin.Frames(), "the cancelled first answer writes nothing")

	second()
	replies := stdin.Requests(t)
	require.Len(t, replies, 1, "the newer answer of a reused request id keeps its reply")
	assert.Equal(t, `"server-2"`, string(replies[0].ID))

	// The first answer's entry must not linger: the id is free for the next use.
	third, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-3", logical, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	third()
	assert.Len(t, stdin.Requests(t), 1, "a cancellation reaches the answer that reused the id")
}

// A cancellation specifies the logical id, the session, AND the workspace. A
// notification that matches on fewer must not cancel anything.
func TestAnswerProviderRuntimeHeaders_CancellationMatchesSessionAndWorkspace(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	const logical = "sess-1:provider-runtime-headers:44444444-5555-4666-8777-888888888888"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	require.Eventually(t, func() bool { return api.mintStarted.Load() >= 1 }, 30*time.Second, time.Millisecond)

	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t,
		"sess-1:provider-runtime-headers:99999999-0000-4000-8000-000000000000", "sess-1", zcodeTestRuntimeWorkspace))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-OTHER", zcodeTestRuntimeWorkspace))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestOtherWorkspace))
	assert.Zero(t, api.mintCancelled.Load(), "no mismatched cancellation aborted the mint")

	unblock()
	replies := zcodeAwaitReplies(t, a.StdinForTest().(*zcodeRecordedStdin), 1)
	assert.Equal(t, `"server-1"`, string(replies[0].ID), "the answer survived every mismatched cancellation")
}

// The workspace match compares JSON by VALUE: a cancellation that states the
// same object in another key order and other whitespace must still withdraw the
// answer, and a semantically different workspace must not.
func TestAnswerProviderRuntimeHeaders_CancellationMatchesTheWorkspaceByValue(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	const reordered = `{ "workspaceKey" : "/tmp/zcode-workspace" , "workspacePath" : "/tmp/zcode-workspace" }`

	const logicalA = "sess-1:provider-runtime-headers:11111111-2222-4333-8444-555555555555"
	first, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-1", logicalA, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logicalA, "sess-1", reordered))
	first()
	assert.Empty(t, stdin.Frames(), "the same workspace object in other bytes cancels the answer")

	const logicalB = "sess-1:provider-runtime-headers:66666666-7777-4888-8999-000000000000"
	second, _ := a.routeZCodeRPC(providerkit.ParseLine(zcodeRuntimeHeadersLine(t,
		"server-2", logicalB, "sess-1", zcodeTestRuntimeWorkspace, "builtin:zai")))
	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logicalB, "sess-1", zcodeTestOtherWorkspace))
	second()
	replies := stdin.Requests(t)
	require.Len(t, replies, 1, "a semantically different workspace cancels nothing")
	assert.Equal(t, `"server-2"`, string(replies[0].ID))
}

// zcodeJSONEqual compares two JSON values semantically and falls back to the
// bytes when either side is not JSON at all. An absent value matches only
// another absent value.
func TestZCodeJSONEqual_ComparesJSONValuesSemantically(t *testing.T) {
	t.Parallel()

	assert.True(t, zcodeJSONEqual([]byte(`{"a":1,"b":[2,3]}`), []byte(`{ "b": [2, 3], "a": 1 }`)))
	assert.False(t, zcodeJSONEqual([]byte(`{"a":1}`), []byte(`{"a":2}`)))
	assert.True(t, zcodeJSONEqual(nil, nil))
	assert.True(t, zcodeJSONEqual([]byte(``), nil))
	assert.False(t, zcodeJSONEqual(nil, []byte(`{}`)))
	assert.True(t, zcodeJSONEqual([]byte(`oops`), []byte(`oops`)))
	assert.False(t, zcodeJSONEqual([]byte(`oops`), []byte(`oops!`)))
}

// The wire cancellation ends the in-flight credential work of its answer.
func TestAnswerProviderRuntimeHeaders_CancellationAbortsTheMint(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	const logical = "sess-1:provider-runtime-headers:5a5a5a5a-5b5b-4c5c-8d5d-5e5e5e5e5e5e"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	require.Eventually(t, func() bool { return api.mintStarted.Load() >= 1 }, 30*time.Second, time.Millisecond)

	a.HandleOutput(zcodeRuntimeHeadersCancelLine(t, logical, "sess-1", zcodeTestRuntimeWorkspace))
	require.Eventually(t, func() bool { return api.mintCancelled.Load() >= 1 },
		zcodeCancelPromptness, time.Millisecond,
		"the cancellation must abort the in-flight mint promptly")
	assert.Empty(t, a.StdinForTest().(*zcodeRecordedStdin).Frames(), "a cancelled answer writes nothing")
}

// A context clear replaces the session: the credential work of the replaced
// session is stale and must end with it.
func TestAnswerProviderRuntimeHeaders_ClearContextCancelsTheReplacedSessionAnswers(t *testing.T) {
	t.Parallel()

	a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	api.set(func(f *zcodeFakeProjectAPI) { f.release = release })
	const logical = "sess-1:provider-runtime-headers:61616161-6262-4363-8464-656565656565"

	a.HandleOutput(zcodeRuntimeHeadersLine(t,
		"server-1", logical, "sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
	require.Eventually(t, func() bool { return api.mintStarted.Load() >= 1 }, 30*time.Second, time.Millisecond)

	stdin := a.StdinForTest().(*zcodeRecordedStdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCreate,
		`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"sessionId":"sess-fresh","eventSeq":0,"events":[]}`)
	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	answerZCodeRequest(t, a, stdin, MethodSessionClose, `{}`)
	_, err := a.ClearContext()
	require.NoError(t, err)

	require.Eventually(t, func() bool { return api.mintCancelled.Load() >= 1 },
		zcodeCancelPromptness, time.Millisecond,
		"the replaced session's credential work must end with the replacement, not at its own timeout")
}

// Stop and Wait tear the agent down: every outstanding credential answer goes
// with it, and its network work ends instead of running out its timeout.
func TestAnswerProviderRuntimeHeaders_StopAndWaitCancelEveryOutstandingAnswer(t *testing.T) {
	t.Parallel()

	for name, teardown := range map[string]func(a *Agent){
		"Stop": func(a *Agent) { a.SimulateExitForTest(); a.Stop() },
		"Wait": func(a *Agent) { a.SimulateExitForTest(); _ = a.Wait() },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, api := zcodeAccountTestAgent(t, "", zcodeTestSignedInStore(t, "login"))
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			api.set(func(f *zcodeFakeProjectAPI) { f.release = release })

			a.HandleOutput(zcodeRuntimeHeadersLine(t,
				"server-1", "sess-1:provider-runtime-headers:71717171-7272-4373-8474-757575757575",
				"sess-1", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
			a.HandleOutput(zcodeRuntimeHeadersLine(t,
				"server-2", "sess-2:provider-runtime-headers:81818181-8282-4383-8484-858585858585",
				"sess-2", zcodeTestRuntimeWorkspace, "account:zai-individual-coding-plan"))
			require.Eventually(t, func() bool { return api.mintStarted.Load() >= 1 }, 30*time.Second, time.Millisecond)

			teardown(a)
			require.Eventually(t, func() bool { return api.mintCancelled.Load() >= 1 },
				zcodeCancelPromptness, time.Millisecond,
				"teardown must end the outstanding credential work promptly, not leave it to its timeout")
		})
	}
}

func TestAnswerOfficialMcpAuthHeaders_DeclaresUnavailable(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)

	a.HandleOutput(zcodeRequestLine(t, 15, MethodRequestOfficialMcpAuthHeaders, `{"server":"docs"}`))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, OfficialAuthUnavailable, result["status"],
		"a declared unavailability lets the app-server fall through to the servers it can reach")
}

func TestAnswerOfficialMcpAuthHeaders_AccountConfigUsesTheCurrentShape(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.account = &zcodeAccountState{}
	a.Mu.Unlock()

	a.HandleOutput(zcodeRequestLine(t, 15, MethodRequestOfficialMcpAuthHeaders, `{}`))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var result map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Result, &result))
	assert.Equal(t, false, result["ok"])
	assert.Equal(t, OfficialAuthUnavailable, result["reason"])
	assert.NotContains(t, result, "status")
}

// An unknown server request is answered with method-not-found rather than ignored,
// for the same reason: the app-server is blocked until something comes back.
func TestHandleZCodeServerRequest_UnknownMethodAnswersMethodNotFound(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)

	a.HandleOutput(zcodeRequestLine(t, 17, "interaction/requestSomethingNew", `{}`))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.NotNil(t, requests[0].Error)
	assert.Equal(t, ErrMethodNotFound, requests[0].Error.Code)
}

// --- the answer paths that must NOT forward a frame ---

// Withholding the forward is the safe answer for every unusable answer: the
// app-server keeps waiting and the user can answer again, rather than receiving a
// frame that means nothing.
func TestZCodeResolveControlResponse_WithholdsAnUnusableForward(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
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
			nil, zcodeAnswer(t, "req-1", agent.ControlBehaviorAllow, "", nil),
		},
		"the stored request is not ours": {
			[]byte(`{"nope":`), zcodeAnswer(t, "req-1", agent.ControlBehaviorAllow, "", nil),
		},
		"the answer is not an allow or a deny": {
			stored, []byte(`{"type":"control_response","request_id":"req-1","response":{}}`),
		},
		"the answer addresses another request": {
			stored, zcodeAnswer(t, "req-OTHER", agent.ControlBehaviorAllow, "", nil),
		},
		"the stored request has no wire id": {
			noWireID, zcodeAnswer(t, "req-1", agent.ControlBehaviorAllow, "", nil),
		},
		"the stored request has no reply shape": {
			[]byte(`{"type":"control_request","request_id":"req-1","id":7,"method":"unknown/method"}`),
			zcodeAnswer(t, "req-1", agent.ControlBehaviorAllow, "", nil),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := zcodeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestPayload:  tc.payload,
				ResponseContent: tc.answer,
			})
			// Withhold, not an empty Content: resolveControlResponsePlan backfills the raw
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
	assert.Equal(t, agent.NotificationKindProviderScoped, bash.Kind())
	assert.Contains(t, bash.Key(), "Bash")
	write := provider.Classify(json.RawMessage(
		`{"type":"permission.resolved","payload":{"toolName":"Write","decision":"deny"}}`))
	assert.NotEqual(t, bash.Key(), write.Key())

	// The latest steer notice occupies its own provider slot.
	queued := provider.Classify(json.RawMessage(`{"type":"` + contracts.ZCodeEventTurnSteerQueued + `"}`))
	drained := provider.Classify(json.RawMessage(`{"type":"` + contracts.ZCodeEventTurnSteerDrained + `"}`))
	assert.Equal(t, agent.ProviderScopedNotification("zcode:steer"), queued)
	assert.Equal(t, queued, drained, "both notices use one provider slot")

	assert.Equal(t, agent.NotificationClassification{}, provider.Classify(json.RawMessage(`{"type":"turn.started"}`)))
	assert.Equal(t, agent.NotificationClassification{}, provider.Classify(json.RawMessage(`not json`)))
}

func TestZCodeProvider_IsInterruptRecognizesTheStopFrame(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.True(t, provider.IsInterrupt(`{"id":1,"method":"session/stop","params":{}}`))
	assert.True(t, provider.IsInterrupt(`{"id":1,"method":"v4/command","params":{"commandId":"leapmux-stop-1","clientId":"leapmux","sessionId":"sess-1","type":"stop","payload":{"expectedForegroundExecutionId":"exec-1"},"issuedAt":0}}`),
		"the guarded v4 stop is the primary interrupt frame")
	assert.False(t, provider.IsInterrupt(`{"id":1,"method":"v4/command","params":{"type":"sendText"}}`))
	assert.False(t, provider.IsInterrupt(`{"id":1,"method":"session/send"}`))
	assert.False(t, provider.IsInterrupt(`not json`))
	assert.False(t, provider.IsInterrupt(``))
}

func TestZCodeProvider_PlanModeControl(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	// Exit, not Prompt: the app-server ASKED and is blocked until it is answered.
	assert.Equal(t, agent.PlanModeControlExit, provider.PlanModeControl(contracts.ZCodeToolNameExitPlanMode))
	assert.Equal(t, agent.PlanModeControlEnter, provider.PlanModeControl(contracts.ZCodeToolNameEnterPlanMode))
	assert.Equal(t, agent.PlanModeControlNone, provider.PlanModeControl("Bash"))
	assert.Equal(t, agent.PlanModeControlNone, provider.PlanModeControl(""))
}

// The permission mode a plan-mode transition lands on is ZCode's OWN word for it.
// Claude's `acceptEdits` is not a value `session/setMode` accepts, so a session told
// that word stays where it was while the settings bar claims otherwise.
func TestZCodeProvider_PlanModePermissionMode(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.Equal(t, contracts.ZCodeModePlan, provider.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.ZCodeModeBuild, provider.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, provider.PlanModePermissionMode(agent.PlanModeControlPrompt),
		"ZCode's plan approval is an Exit, never a server-side Prompt")
	assert.Empty(t, provider.PlanModePermissionMode(agent.PlanModeControlNone))

	// Every mode this returns must be one the app-server's own enumeration carries, or
	// the next launch sends a mode session/setMode rejects.
	modes := map[string]bool{contracts.ZCodeModePlan: true, contracts.ZCodeModeBuild: true, contracts.ZCodeModeEdit: true, contracts.ZCodeModeYolo: true}
	for _, kind := range []agent.PlanModeControlKind{agent.PlanModeControlEnter, agent.PlanModeControlExit} {
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

	// The Worker's own count, which every divider carries, outranks the native one: a
	// stopped turn states toolCallCount 0 for the call that the stop cut.
	count, ok = provider.TurnEndToolUses([]byte(`{"type":"turn.completed","payload":{"toolCallCount":0,"resultType":"cancelled"},"num_tool_uses":1}`))
	assert.True(t, ok)
	assert.Equal(t, int32(1), count)
	count, ok = provider.TurnEndToolUses([]byte(`{"type":"turn.failed","payload":{"toolCallCount":3},"num_tool_uses":2}`))
	assert.True(t, ok)
	assert.Equal(t, int32(2), count)
}

// ZCode's handle is an opaque TOKEN from session/list, so the token rule applies and
// the workspace path is irrelevant to it. A path-based provider resolves its handle
// against the workspace instead; ZCode must NOT, because a token is not a file.
func TestZCodeProvider_ResolveResumeHandleTakesATokenNotAPath(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	require.NoError(t, agenttest.ResumeHandleErr(provider, "01JQ8Z4T5N6P7R8S9T0V1W2X3Y", "/tmp/work"))
	require.NoError(t, agenttest.ResumeHandleErr(provider, "", "/tmp/work"),
		"an empty handle means no resume, which is not an error")
	require.NoError(t, agenttest.ResumeHandleErr(provider, "01JQ8Z4T5N6P7R8S9T0V1W2X3Y", ""),
		"the workspace does not participate: a token is resolved by the app-server, not on disk")

	// The token rule refuses what would corrupt the frame or the stored row.
	assert.Error(t, agenttest.ResumeHandleErr(provider, "has\na newline", "/tmp/work"))
	assert.Error(t, agenttest.ResumeHandleErr(provider, "  padded  ", "/tmp/work"))
	assert.Error(t, agenttest.ResumeHandleErr(provider, strings.Repeat("x", 4096), "/tmp/work"))
}

// A binary file has no kind that keeps its bytes, so a refusal that says so is
// better than a send that loses it.
func TestZCodeProvider_ValidateAttachmentRefusesWhatTheAppServerCannotCarry(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	require.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindText, Filename: "a.txt"}))
	require.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindImage, Filename: "a.png"}),
		"the image gate depends on the running model, so it is not enforced here")
	require.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindPdf, Filename: "spec.pdf"}),
		"the PDF gate depends on the running model, so it is not enforced here")

	err := provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindBinary, Filename: "a.bin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a.bin", "the message must name the file the user attached")
}

func TestZCodeProvider_TheFalseCapabilitiesAreDeliberate(t *testing.T) {
	t.Parallel()

	provider := zcodeProvider{}
	assert.False(t, provider.IsSelfDisplayingControlTool(contracts.ZCodeToolNameAskUserQuestion),
		"the app-server echoes no control answer, so the synthetic row is the only record")
	assert.False(t, provider.ChildCapabilities(nil).AcceptsMessages, "a ZCode subagent takes no further message")
	count, known := provider.TurnEndToolUses(nil)
	assert.False(t, known)
	assert.Zero(t, count)
	assert.Empty(t, provider.PlanApprovalOptions(""))
	assert.Empty(t, provider.SyntheticInterruptNotice())
	_, ok := provider.PermissionModeFromRawInput(`{"mode":"yolo"}`)
	assert.False(t, ok, "ZCode's mode changes ride session/setMode, never a raw stdin frame")
	assert.Equal(t, contracts.ZCodeDefaultMode,
		Registration().PermissionDefaults.Fallback)
}

func TestZCodeProvider_IsRegistered(t *testing.T) {
	t.Parallel()

	assert.IsType(t, zcodeProvider{}, Registration().Plugin)
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

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 8, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))

	persisted := sink.PublishedControls()
	require.Len(t, persisted, 3)
	for i := 1; i < len(persisted); i++ {
		assert.Equal(t, string(persisted[0].Payload), string(persisted[i].Payload),
			"a repeat must republish the stored bytes, or the frontend stacks a banner")
	}

	// The FIRST wire id is the one every stored payload addresses, and it stays valid:
	// the app-server registers every announced id against the same pending request.
	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(persisted[2].Payload, &stored))
	assert.JSONEq(t, `7`, string(stored.WireID))
}

func TestHandlePermissionRequest_AResolvedRequestIDIsForwardedAgain(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventPermissionResolved,
		`{"requestId":"req-1","toolCallId":"call-1","toolName":"Bash","decision":"allow"}`))
	a.HandleOutput(zcodeRequestLine(t, 8, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))

	assert.Len(t, sink.PublishedControls(), 2,
		"the guard is re-armed by the resolution, so a genuinely new prompt on the same id is shown")
}

// Same rule for a question or a plan approval. See the permission twin above.
func TestHandleUserInputRequest_AReannouncementRepublishesTheFirstPayload(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))
	a.HandleOutput(zcodeRequestLine(t, 12, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))

	persisted := sink.PublishedControls()
	require.Len(t, persisted, 2)
	assert.Equal(t, string(persisted[0].Payload), string(persisted[1].Payload))

	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(persisted[1].Payload, &stored))
	assert.JSONEq(t, `11`, string(stored.WireID))
}

func TestHandleUserInputRequest_UserInputResolvedReArmsTheGuard(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))

	a.HandleOutput(zcodeRequestLine(t, 11, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventUserInputResolved, `{"requestId":"req-q"}`))
	a.HandleOutput(zcodeRequestLine(t, 12, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))

	assert.Len(t, sink.PublishedControls(), 2)
	assert.Empty(t, sink.PersistedNotifications(), "userInput.resolved renders nothing of its own")
}

// A request with no id cannot be de-duplicated, and it is answered rather than
// forwarded -- so the guard must not swallow the second one.
func TestHandlePermissionRequest_AnIDLessRequestIsAlwaysAnswered(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)

	params := `{"sessionId":"sess-1","toolName":"Bash","options":[{"optionId":"deny","kind":"deny","response":{"decision":"deny"}}]}`
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, params))
	a.HandleOutput(zcodeRequestLine(t, 8, contracts.ZCodeMethodRequestPermission, params))

	assert.Empty(t, sink.PublishedControls())
	assert.Len(t, stdin.Requests(t), 2, "each unroutable request gets its own denial")
}

// --- permission bookkeeping ---

func TestZCodePermissionBookkeeping_ForgetIsOneShot(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))

	assert.False(t, a.forgetZCodeControlRequest("req-1"), "an unknown id was never forwarded")
	assert.False(t, a.forgetZCodeControlRequest(""))

	zcodeRememberControlForTest(t, a, "req-1")
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

	answers := zcodeAnswersFromResponse(zcodeAnswer(t, "req-q", agent.ControlBehaviorAllow, "",
		map[string]string{"q": "a"}))
	assert.Equal(t, map[string]string{"q": "a"}, answers)

	assert.Nil(t, zcodeAnswersFromResponse([]byte(`not json`)))
	assert.Nil(t, zcodeAnswersFromResponse(zcodeAnswer(t, "req-q", agent.ControlBehaviorAllow, "", nil)))
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

// --- MCP input forms ---

// zcodeMCPFormSchema is an MCP server's form. The array `tags` sits between two
// fields that ZCode carries, and ZCode skips it.
const zcodeMCPFormSchema = `{"type":"object","properties":{
  "count":{"type":"integer","title":"Count","minimum":0,"maximum":3},
  "tags":{"type":"array","items":{"type":"string"}},
  "enabled":{"type":"boolean","title":"Enabled"},
  "color":{"type":"string","title":"Color","oneOf":[{"const":"b","title":"Blue"},{"const":"r","title":"Red"}]}
},"required":["count","enabled","color"]}`

// zcodeMCPFormParams is the user-input request in which ZCode 3.16.3 forwards that
// form, in the shape the live probe recorded: the schema, and the questions that
// ZCode flattens it to.
var zcodeMCPFormParams = `{
  "requestId": "mcp-elicit-1",
  "sessionId": "sess-1",
  "toolCallId": "mcp-elicit-1",
  "toolName": "McpElicitation",
  "prompt": "Choose the probe settings.",
  "input": {"serverName": "form_probe", "message": "Choose the probe settings.", "requestedSchema": ` + zcodeMCPFormSchema + `},
  "schema": {"interaction": "mcp_elicitation", "serverName": "form_probe", "requestedSchema": ` + zcodeMCPFormSchema + `},
  "questions": [
    {"header": "Count", "question": "Choose the probe settings.\n\nCount", "options": [{"value": "__zcode_free_text__", "label": "Type your answer"}]},
    {"header": "Enabled", "question": "Choose the probe settings.\n\nEnabled", "options": [{"value": "true", "label": "Yes"}, {"value": "false", "label": "No"}]},
    {"header": "Color", "question": "Choose the probe settings.\n\nColor", "options": [{"value": "__zcode_free_text__", "label": "Type your answer"}]}
  ]
}`

// zcodeMCPFormAnswer is the browser's answer to an MCP form.
func zcodeMCPFormAnswer(t *testing.T, requestID string, answer map[string]any) []byte {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"request_id": requestID, "response": answer},
	})
	require.NoError(t, err)
	return content
}

// storeZCodeMCPForm feeds the form request and returns the stored control payload.
func storeZCodeMCPForm(t *testing.T, params string) []byte {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeRequestLine(t, 21, contracts.ZCodeMethodRequestUserInput, params))
	return zcodeStoredPayload(t, sink)
}

func TestHandleUserInputRequest_MCPFormIsStoredAsAForm(t *testing.T) {
	t.Parallel()
	var stored zcodeControlRequestPayload
	require.NoError(t, json.Unmarshal(storeZCodeMCPForm(t, zcodeMCPFormParams), &stored))
	assert.Equal(t, zcodeControlMCPElicitation, stored.Request.ToolName, "the form stays off the question surface")
	assert.Equal(t, "mcp-elicit-1", stored.RequestID)
	assert.JSONEq(t, `21`, string(stored.WireID))
	var params struct {
		Schema struct {
			Interaction     string          `json:"interaction"`
			ServerName      string          `json:"serverName"`
			RequestedSchema json.RawMessage `json:"requestedSchema"`
		} `json:"schema"`
	}
	require.NoError(t, json.Unmarshal(stored.Params, &params))
	assert.Equal(t, contracts.ZCodeInteractionMcpElicitation, params.Schema.Interaction)
	assert.Equal(t, "form_probe", params.Schema.ServerName)
	assert.JSONEq(t, zcodeMCPFormSchema, string(params.Schema.RequestedSchema), "the browser draws the form from the native schema")
	assert.False(t, zcodeProvider{}.IsSelfDisplayingControlTool(stored.Request.ToolName))
}

// An accepted form sends each value as the text that ZCode reads back, at the
// place of its field among the fields that ZCode carries.
func TestResolveMCPForm_AcceptSendsTypedAnswersPositionally(t *testing.T) {
	t.Parallel()
	stored := storeZCodeMCPForm(t, zcodeMCPFormParams)
	frame, res := zcodeResolve(t, stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{
		"action":  contracts.MCPElicitationActionAccept,
		"content": map[string]any{"count": 0, "tags": []string{"left out"}, "enabled": false, "color": "b"},
	}))
	assert.NoError(t, res.Refusal())
	assert.JSONEq(t, `21`, string(frame.ID), "the reply answers the request's own wire id")
	assert.JSONEq(t, `{"action":"accept","content":{"answer_0":"0","answer_1":"false","answer_2":"b"}}`, string(frame.Result))

	frame, _ = zcodeResolve(t, stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{
		"action":  contracts.MCPElicitationActionAccept,
		"content": map[string]any{"count": 2.5e0, "enabled": true, "color": ""},
	}))
	assert.JSONEq(t, `{"action":"accept","content":{"answer_0":"2.5","answer_1":"true","answer_2":""}}`, string(frame.Result))

	frame, _ = zcodeResolve(t, stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{"action": contracts.MCPElicitationActionAccept}))
	assert.JSONEq(t, `{"action":"accept","content":{}}`, string(frame.Result), "an empty form answers nothing")
}

// A request that states its schema only in the input reads it from there.
func TestResolveMCPForm_ReadsTheSchemaOfTheInput(t *testing.T) {
	t.Parallel()
	params := `{"requestId":"mcp-elicit-2","sessionId":"sess-1","toolName":"McpElicitation",
	  "input":{"requestedSchema":{"type":"object","properties":{"note":{"type":"string"},"level":{"enum":["low","high"]}}}},
	  "schema":{"interaction":"mcp_elicitation"}}`
	frame, _ := zcodeResolve(t, storeZCodeMCPForm(t, params), zcodeMCPFormAnswer(t, "mcp-elicit-2", map[string]any{
		"action": contracts.MCPElicitationActionAccept, "content": map[string]any{"level": "high", "note": "n"},
	}))
	assert.JSONEq(t, `{"action":"accept","content":{"answer_0":"n","answer_1":"high"}}`, string(frame.Result))
}

func TestResolveMCPForm_DeclineAndCancelCarryNoContent(t *testing.T) {
	t.Parallel()
	stored := storeZCodeMCPForm(t, zcodeMCPFormParams)
	for action, want := range map[string]string{
		contracts.MCPElicitationActionDecline: contracts.ZCodeActionDecline,
		contracts.MCPElicitationActionCancel:  contracts.ZCodeActionCancel,
	} {
		frame, res := zcodeResolve(t, stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{
			"action": action, "content": map[string]any{"count": 1},
		}))
		assert.NoError(t, res.Refusal(), action)
		assert.JSONEq(t, fmt.Sprintf(`{"action":%q}`, want), string(frame.Result), action)
	}
}

// An answer that the form cannot take sends nothing, so ZCode keeps waiting and
// the reader can answer again.
func TestResolveMCPForm_RefusesAnAnswerTheFormCannotTake(t *testing.T) {
	t.Parallel()
	stored := storeZCodeMCPForm(t, zcodeMCPFormParams)
	var withoutWireID map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored, &withoutWireID))
	delete(withoutWireID, "id")
	storedWithoutWireID, err := json.Marshal(withoutWireID)
	require.NoError(t, err)
	accept := map[string]any{"action": contracts.MCPElicitationActionAccept, "content": map[string]any{"count": 1}}
	for name, tc := range map[string]struct {
		stored []byte
		answer []byte
	}{
		"another request":         {stored, zcodeMCPFormAnswer(t, "mcp-elicit-other", accept)},
		"an unreadable answer":    {stored, []byte(`{"response":`)},
		"an allow decision":       {stored, zcodeAnswer(t, "mcp-elicit-1", agent.ControlBehaviorAllow, "", nil)},
		"an unknown action":       {stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{"action": "maybe"})},
		"content that is no form": {stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{"action": contracts.MCPElicitationActionAccept, "content": []int{1}})},
		"a persisted choice":      {stored, zcodeMCPFormAnswer(t, "mcp-elicit-1", map[string]any{"action": contracts.MCPElicitationActionAccept, "_meta": map[string]string{"persist": "always"}})},
		"no wire id":              {storedWithoutWireID, zcodeMCPFormAnswer(t, "mcp-elicit-1", accept)},
	} {
		res := zcodeProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: tc.stored, ResponseContent: tc.answer})
		assert.True(t, res.Withhold, name)
		assert.Error(t, res.Refusal(), name)
	}
}

// ZCode carries a property with an `enum` array or of a scalar type, in the
// order of `properties`, and counts its positional answers over those alone.
func TestZCodeElicitationFields(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		schema string
		want   []string
	}{
		"each carried type": {
			`{"properties":{"a":{"type":"string"},"b":{"type":"number"},"c":{"type":"integer"},"d":{"type":"boolean"},"e":{"enum":["x",1]}}}`,
			[]string{"a", "b", "c", "d", "e"},
		},
		"skipped properties keep the order of the rest": {
			`{"properties":{"z":{"type":"string"},"arr":{"type":"array"},"obj":{"type":"object"},"none":{},"null":null,"text":"string","y":{"type":"boolean"}}}`,
			[]string{"z", "y"},
		},
		"an enum that is no array": {`{"properties":{"e":{"enum":"x"}}}`, nil},
		"a type that is no string": {`{"properties":{"t":{"type":["string","null"]}}}`, nil},
		"a property stated twice keeps its first place and its last definition": {
			`{"properties":{"a":{"type":"array"},"b":{"type":"string"},"a":{"type":"string"}}}`,
			[]string{"a", "b"},
		},
		"properties that are no object": {`{"properties":["a"]}`, nil},
		"no properties":                 {`{"type":"object"}`, nil},
		"no schema":                     {``, nil},
		"a schema that is no object":    {`"form"`, nil},
	} {
		assert.Equal(t, tc.want, zcodeElicitationFields(json.RawMessage(tc.schema)), name)
	}
}

func TestZCodeElicitationAnswerText(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		`"blue"`: "blue", `""`: "", `0`: "0", `-1.25`: "-1.25", `1e2`: "1e2",
		`12345678901234567890`: "12345678901234567890", `true`: "true", `false`: "false",
	} {
		text, ok := zcodeElicitationAnswerText(json.RawMessage(raw))
		assert.True(t, ok, raw)
		assert.Equal(t, want, text, raw)
	}
	for _, raw := range []string{``, ` `, `null`, `[1]`, `{"a":1}`, `{`} {
		_, ok := zcodeElicitationAnswerText(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestZCodeControls_CorrelateRepeatedNativeIDsByTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	for index, turnID := range []string{"foreground", "background"} {
		source := ""
		if index > 0 {
			source = "background_task"
		}
		a.HandleOutput(zcodeTurnEventLine(t, int64(index*2+1), turnID, contracts.ZCodeEventTurnStarted, fmt.Sprintf(`{"turnNumber":%d,"inputSource":%q}`, index+1, source)))
		a.HandleOutput(zcodeTurnEventLine(t, int64(index*2+2), turnID, contracts.ZCodeEventToolUpdated, fmt.Sprintf(`{"kind":"scheduled","toolCallId":"same","assistantMessageId":"message-%d","toolName":"Bash","inputOmitted":true}`, index)))
	}
	openings := sink.Messages()
	require.Len(t, openings, 2)
	for index, turnID := range []string{"foreground", "background"} {
		params := json.RawMessage(fmt.Sprintf(`{"requestId":"request-%d","sessionId":"sess-1","turnId":%q,"toolCallId":"same","toolName":"Bash","input":{"command":"command-%d"}}`, index, turnID, index))
		a.handlePermissionRequest(json.RawMessage(fmt.Sprintf(`"rpc-%d"`, index)), params)
		stored, err := a.sink.ReadToolRequest(openings[index].SpanID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		resolved := zcodeProvider{}.ResolveProviderData(stored.Content)
		var frame struct {
			Payload struct {
				Input struct {
					Command string `json:"command"`
				} `json:"input"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(resolved, &frame))
		assert.Equal(t, fmt.Sprintf("command-%d", index), frame.Payload.Input.Command)
	}
}

func TestZCodeControls_ResolveRawChildCallIDsFromOrigin(t *testing.T) {
	t.Parallel()
	a, sink := spawnAZCodeSubagent(t)
	// Mirrored events qualify the call ID; the interaction RPC preserves its raw ID.
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","source":"subagent","agentId":"agent-1","childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1","toolCallId":"tool_subagent_agent-1_raw","assistantMessageId":"child-message","toolName":"Bash","inputOmitted":true}`))
	a.handlePermissionRequest(json.RawMessage(`"rpc"`), json.RawMessage(`{"requestId":"child-request","sessionId":"sess-1","toolCallId":"raw","toolName":"Bash","input":{"command":"pwd"},"origin":{"kind":"subagent","agentId":"agent-1","childSessionId":"sess_subagent_agent-1","parentToolCallId":"spawn-1"}}`))
	child := sink.Child(sink.ChildAgentIDs()[0])
	spanID := zcodeTestSpanID(t, sink, "tool_subagent_agent-1_raw")
	stored, err := child.ReadToolRequest(spanID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	var frame struct {
		Payload struct {
			Input map[string]string `json:"input"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(zcodeProvider{}.ResolveProviderData(stored.Content), &frame))
	assert.Equal(t, "pwd", frame.Payload.Input["command"])
}

func TestZCodeJSONEqual_PreservesExactNumericIdentity(t *testing.T) {
	t.Parallel()
	assert.False(t, zcodeJSONEqual([]byte(`{"id":9007199254740992}`), []byte(`{"id":9007199254740993}`)))
	assert.True(t, zcodeJSONEqual([]byte(`{"value":1.0,"s":"\u0061"}`), []byte(`{"s":"a","value":1e0}`)))
	assert.True(t, zcodeJSONEqual([]byte(`-0e999999999999999999999`), []byte(`0`)))
	assert.True(t, zcodeJSONEqual([]byte(`10e999999999999999999999`), []byte(`1e1000000000000000000000`)))
}

func TestZCodeRuntimeHeaderReply_ReleasesCompletedContexts(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("write failure %t", failure), func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
			answer := a.prepareZCodeRuntimeHeaderAnswer(json.RawMessage(`1`), json.RawMessage(`{"requestId":"logical","sessionId":"sess-1"}`))
			require.NotNil(t, answer)
			if failure {
				a.SetStdinForTest(agenttest.NopStdin(zcodeInputWriter(func([]byte) (int, error) { return 0, errors.New("failed write") })))
			}
			a.replyZCodeRuntimeHeader(json.RawMessage(`1`), answer, map[string]any{})
			assert.Error(t, answer.ctx.Err(), "every completed answer must release its process-context registration")
		})
	}
}

func TestZCodeControlIngress_RetiresBeforeTheHandlerRuns(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.ZCodeMethodRequestPermission, contracts.ZCodeMethodRequestUserInput} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s replacement %t", method, replacement), func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.ControlSink{}
				a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
				raw := zcodeRequestLine(t, 7, method, `{"requestId":"delayed","toolName":"Bash","input":{},"options":[],"questions":[]}`)
				handler, consumed := a.routeZCodeRPC(providerkit.ParseLine(raw))
				require.True(t, consumed)
				require.NotNil(t, handler)
				if replacement {
					snapshot, ok := a.parseStateSnapshot(json.RawMessage(`{"session":{"sessionId":"sess-1"}}`))
					require.True(t, ok)
					a.adoptCompleteZCodeSessionSnapshot(snapshot, true)
				} else {
					a.forgetZCodeControlRequest("delayed")
				}
				handler()
				assert.Empty(t, sink.PublishedControls(), "a retired ingress instance must not publish a new prompt")
				a.Mu.Lock()
				assert.Empty(t, a.pendingControls)
				a.Mu.Unlock()
			})
		}
	}
}

type zcodeHeldPlanSink struct {
	*agenttest.ControlSink
	entered chan struct{}
	release chan struct{}
}

func (sink *zcodeHeldPlanSink) holdPlan(title string) {
	if title == "Old plan" {
		close(sink.entered)
		<-sink.release
	}
}
func (sink *zcodeHeldPlanSink) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) {
	sink.holdPlan(title)
	sink.ControlSink.UpdatePlan(content, compression, title)
}

func TestZCodePlanControl_CannotOverwriteTheReplacementPlan(t *testing.T) {
	t.Parallel()
	sink := &zcodeHeldPlanSink{ControlSink: &agenttest.ControlSink{}, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
	})
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	raw := zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, `{"requestId":"old-plan","toolName":"ExitPlanMode","input":{"plan":"# Old plan"},"schema":{"interaction":"plan_approval"}}`)
	handled := make(chan struct{})
	go func() { a.HandleOutput(raw); close(handled) }()
	zcodeTestReceive(t, sink.entered)
	snapshot, ok := a.parseStateSnapshot(json.RawMessage(`{"session":{"sessionId":"sess-1"}}`))
	require.True(t, ok)
	a.adoptCompleteZCodeSessionSnapshot(snapshot, true)
	sink.ControlSink.UpdatePlan([]byte("# New plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "New plan")
	close(sink.release)
	zcodeTestReceive(t, handled)
	assert.Equal(t, "New plan", sink.LastPlanUpdate().Title)
	assert.Empty(t, sink.PublishedControls())
}

type zcodeHeldCapturedPlanWriter struct {
	sink     *zcodeHeldPlanSink
	delegate agent.CapturedPlanWriter
}

func (writer zcodeHeldCapturedPlanWriter) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) error {
	writer.sink.holdPlan(title)
	return writer.delegate.UpdatePlan(content, compression, title)
}
func (sink *zcodeHeldPlanSink) PlanWriterFor(captured agent.CapturedTranscript) (agent.CapturedPlanWriter, error) {
	delegate, err := sink.ControlSink.PlanWriterFor(captured)
	if err != nil {
		return nil, err
	}
	return zcodeHeldCapturedPlanWriter{sink: sink, delegate: delegate}, nil
}

func TestZCodeControlPreparation_DoesNotBlockAnotherRequest(t *testing.T) {
	t.Parallel()
	sink := &zcodeHeldPlanSink{ControlSink: &agenttest.ControlSink{}, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
	})
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	raw := zcodeRequestLine(t, 9, contracts.ZCodeMethodRequestUserInput, `{"requestId":"old-plan","toolName":"ExitPlanMode","input":{"plan":"# Old plan"},"schema":{"interaction":"plan_approval"}}`)
	handled := make(chan struct{})
	go func() { a.HandleOutput(raw); close(handled) }()
	zcodeTestReceive(t, sink.entered)
	questionDone := make(chan struct{})
	go func() {
		a.handlePermissionRequest(json.RawMessage(`10`), json.RawMessage(`{"requestId":"other","toolName":"Bash","input":{}}`))
		close(questionDone)
	}()
	ctx := testutil.DeadlineContext(t)
	completed := false
	select {
	case <-questionDone:
		completed = true
	case <-ctx.Done():
		assert.Fail(t, "the first control handler held a lock through plan storage")
	}
	if completed {
		assert.Len(t, sink.PublishedControls(), 1)
	}
	close(sink.release)
	if !completed {
		zcodeTestReceive(t, questionDone)
	}
	zcodeTestReceive(t, handled)
	assert.Len(t, sink.PublishedControls(), 2)
}

type zcodeReentrantCaptureSink struct {
	*agenttest.ControlSink
	entered    chan struct{}
	release    chan struct{}
	nestedDone chan struct{}
	once       atomic.Bool
	nested     func()
}

func (sink *zcodeReentrantCaptureSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	if sink.once.CompareAndSwap(false, true) {
		go func() { sink.nested(); close(sink.nestedDone) }()
		close(sink.entered)
		<-sink.release
	}
	return sink.ControlSink.CaptureMessage(content, span)
}

func TestZCodeControlIngress_ReentrantCaptureDoesNotWaitForItself(t *testing.T) {
	t.Parallel()
	sink := &zcodeReentrantCaptureSink{ControlSink: &agenttest.ControlSink{}, entered: make(chan struct{}), release: make(chan struct{}), nestedDone: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
	})
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	params := `{"requestId":"same","toolName":"Bash","input":{}}`
	sink.nested = func() { a.handlePermissionRequest(json.RawMessage(`2`), json.RawMessage(params)) }
	done := make(chan struct{})
	go func() { a.handlePermissionRequest(json.RawMessage(`1`), json.RawMessage(params)); close(done) }()
	zcodeTestReceive(t, sink.entered)
	completed := false
	ctx := testutil.DeadlineContext(t)
	select {
	case <-sink.nestedDone:
		completed = true
	case <-ctx.Done():
		assert.Fail(t, "a repeated request waited for the capture that invoked it")
	}
	close(sink.release)
	if !completed {
		zcodeTestReceive(t, sink.nestedDone)
	}
	zcodeTestReceive(t, done)
	published := sink.PublishedControls()
	require.Len(t, published, 2)
	assert.Equal(t, published[0].Payload, published[1].Payload)
}

type zcodeReentrantPublicationSink struct {
	*agenttest.ControlSink
	once    atomic.Bool
	reenter func()
}

func (sink *zcodeReentrantPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	if sink.once.CompareAndSwap(false, true) {
		sink.reenter()
	}
	return sink.ControlSink.PublishControlRequest(request)
}

func TestZCodeControlPublication_OrdersReusedLogicalInstances(t *testing.T) {
	t.Parallel()
	sink := &zcodeReentrantPublicationSink{ControlSink: &agenttest.ControlSink{}}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	var replacement string
	sink.reenter = func() {
		require.True(t, a.forgetZCodeControlRequest("same"))
		a.handlePermissionRequest(json.RawMessage(`2`), json.RawMessage(`{"requestId":"same","toolName":"Bash","input":{"command":"new"}}`))
		a.Mu.Lock()
		replacement = a.pendingControls["same"].instanceID
		a.Mu.Unlock()
	}
	a.handlePermissionRequest(json.RawMessage(`1`), json.RawMessage(`{"requestId":"same","toolName":"Bash","input":{"command":"old"}}`))
	published := sink.PublishedControls()
	require.Len(t, published, 2)
	assert.Equal(t, replacement, published[1].InstanceID, "the new instance must store after the retired publication returns")
	assert.Contains(t, string(published[1].Payload), `"command":"new"`)
}

func TestZCodeControlIngress_AnswersAnUnreadySessionWithoutPublishing(t *testing.T) {
	t.Parallel()
	for _, method := range []string{contracts.ZCodeMethodRequestPermission, contracts.ZCodeMethodRequestUserInput} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			a.Mu.Lock()
			a.sessionChanging = true
			a.Mu.Unlock()
			a.HandleOutput(zcodeRequestLine(t, 7, method, `{"requestId":"unready","sessionId":"sess-1","toolName":"Bash","input":{"plan":"# Test"},"schema":{"interaction":"plan_approval"}}`))
			require.Len(t, stdin.Frames(), 1, "a refused server request must receive a reply")
			var response zcodeResponseEnvelope
			require.NoError(t, json.Unmarshal(stdin.Frames()[0], &response))
			require.NotNil(t, response.Error)
			assert.Empty(t, sink.PublishedControls())
			a.Mu.Lock()
			assert.Empty(t, a.pendingControls)
			a.Mu.Unlock()
		})
	}
}
