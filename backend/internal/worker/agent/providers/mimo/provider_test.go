package mimo

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storedPermissionPayload is a permission request as publishControl stores it.
const storedPermissionPayload = `{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_test","permission":"bash"},` +
	`"request":{"tool_name":"bash","tool_use_id":"call-1"}}`

// storedQuestionPayload is a question as publishControl stores it.
const storedQuestionPayload = `{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_test","questions":[{"question":"Pick one"}]},` +
	`"request":{"tool_name":"question","tool_use_id":"call-2"}}`

// storedPlanPayload is a plan approval as publishControl stores it.
const storedPlanPayload = `{"type":"question.asked","properties":{"id":"que_2","sessionID":"ses_test","questions":[{"key":"plan_exit","params":{"plan":"plan.md"}}]},` +
	`"request":{"tool_name":"plan_exit","tool_use_id":"call-3"},"plan":"# Plan"}`

// storedElicitationPayload is an MCP server's confirmation as publishControl
// stores it: MiMo HEAD asks it as one question, with MiMo's three answers.
const storedElicitationPayload = `{"type":"question.asked","properties":{"id":"que_3","sessionID":"ses_test","questions":[{"key":"mcp_elicitation",` +
	`"header":"docs","question":"docs\n\nProceed?","options":[{"label":"Accept"},{"label":"Decline"},{"label":"Cancel"}],"multiple":false,"custom":false}]},` +
	`"request":{"tool_name":"question"}}`

// elicitationEnvelope is the answer that the browser's shared elicitation form
// writes, with the MCP action the reader chose.
func elicitationEnvelope(requestID, action string) []byte {
	raw, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID, "response": map[string]any{"action": action, "content": map[string]any{}},
	}})
	if err != nil {
		panic(err)
	}
	return raw
}

func allowEnvelope(requestID string) []byte {
	return []byte(`{"response":{"request_id":"` + requestID + `","response":{"behavior":"allow"}}}`)
}

func denyEnvelope(requestID, message string) []byte {
	raw, err := json.Marshal(map[string]any{"response": map[string]any{
		"request_id": requestID, "response": map[string]any{"behavior": "deny", "message": message},
	}})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestResolveControlResponseConformance(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, mimoProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, mimoProvider{})
}

func TestResolveControlResponse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		requestID string
		payload   string
		response  []byte
		withhold  bool
		planExit  bool
	}{
		{name: "an allow answers a permission", requestID: "mimo-permission:per_1", payload: storedPermissionPayload,
			response: allowEnvelope("mimo-permission:per_1")},
		{name: "a chosen option answers a permission", requestID: "mimo-permission:per_1", payload: storedPermissionPayload,
			response: []byte(`{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"always"}}}`)},
		{name: "an unknown option is withheld", requestID: "mimo-permission:per_1", payload: storedPermissionPayload,
			response: []byte(`{"jsonrpc":"2.0","id":"mimo-permission:per_1","result":{"outcome":{"outcome":"selected","optionId":"forever"}}}`),
			withhold: true},
		{name: "answers answer a question", requestID: "mimo-question:que_1", payload: storedQuestionPayload,
			response: []byte(`{"jsonrpc":"2.0","id":"mimo-question:que_1","result":{"answers":[["A"]]}}`)},
		{name: "an allow does not answer a question", requestID: "mimo-question:que_1", payload: storedQuestionPayload,
			response: allowEnvelope("mimo-question:que_1"), withhold: true},
		{name: "an elicitation action answers an MCP server's confirmation", requestID: "mimo-question:que_3", payload: storedElicitationPayload,
			response: elicitationEnvelope("mimo-question:que_3", contracts.MCPElicitationActionDecline)},
		// The browser writes this envelope for an elicitation alone. A question that
		// does not offer MiMo's word for the action cannot take it.
		{name: "an elicitation action to a question that does not offer it is withheld", requestID: "mimo-question:que_1", payload: storedQuestionPayload,
			response: elicitationEnvelope("mimo-question:que_1", contracts.MCPElicitationActionAccept), withhold: true},
		{name: "an elicitation action to a permission is withheld", requestID: "mimo-permission:per_1", payload: storedPermissionPayload,
			response: elicitationEnvelope("mimo-permission:per_1", contracts.MCPElicitationActionAccept), withhold: true},
		{name: "an elicitation action to a plan is withheld", requestID: "mimo-question:que_2", payload: storedPlanPayload,
			response: elicitationEnvelope("mimo-question:que_2", contracts.MCPElicitationActionAccept), withhold: true, planExit: true},
		{name: "an allow approves a plan", requestID: "mimo-question:que_2", payload: storedPlanPayload,
			response: allowEnvelope("mimo-question:que_2"), planExit: true},
		{name: "a deny with feedback rejects a plan", requestID: "mimo-question:que_2", payload: storedPlanPayload,
			response: denyEnvelope("mimo-question:que_2", "Add tests"), planExit: true},
		{name: "an answer to another request is withheld", requestID: "mimo-permission:per_1", payload: storedPermissionPayload,
			response: allowEnvelope("mimo-permission:per_9"), withhold: true},
		{name: "a request of an unknown type is withheld", requestID: "mimo-permission:per_1", payload: `{"type":"session.status"}`,
			response: allowEnvelope("mimo-permission:per_1"), withhold: true},
		{name: "a request that is gone is withheld", requestID: "mimo-permission:per_1", payload: "",
			response: allowEnvelope("mimo-permission:per_1"), withhold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := mimoProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestID:       tc.requestID,
				RequestPayload:  json.RawMessage(tc.payload),
				ResponseContent: tc.response,
			})
			assert.Equal(t, tc.withhold, res.Withhold)
			assert.Equal(t, tc.response, res.Content, "the answer keeps the shape the browser wrote")
			if tc.planExit {
				assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
			} else {
				assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
			}
		})
	}
}

func TestPlanModeControl(t *testing.T) {
	t.Parallel()
	provider := mimoProvider{}
	assert.Equal(t, agent.PlanModeControlExit, provider.PlanModeControl(contracts.MiMoToolPlanExit))
	assert.Equal(t, agent.PlanModeControlNone, provider.PlanModeControl(contracts.MiMoToolQuestion))
	assert.Equal(t, agent.PlanModeControlNone, provider.PlanModeControl(""))
	assert.Equal(t, contracts.MiMoModeBuild, provider.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Equal(t, contracts.MiMoModePlan, provider.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Empty(t, provider.PlanModePermissionMode(agent.PlanModeControlNone))
}

func TestSupportsChildSteering(t *testing.T) {
	t.Parallel()
	assert.True(t, mimoProvider{}.SupportsChildSteering())
}

func TestClassify(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want agent.NotificationClassification
	}{
		{
			name: "a retry folds into the latest attempt",
			raw:  `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"retry","attempt":2,"message":"overloaded","next":5}}}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "mimo:retry"},
		},
		{
			name: "an idle status is no notification kind",
			raw:  `{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"idle"}}}`,
		},
		{
			name: "a compaction that starts is a status",
			raw:  `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","type":"compaction","auto":true}}}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "mimo:compaction"},
		},
		{
			name: "a compaction with a null projection still starts",
			raw:  `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","type":"compaction","projection":null}}}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "mimo:compaction"},
		},
		{
			name: "a compaction that ends is the boundary",
			raw:  `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","type":"compaction","projection":{"summary":"s"}}}}`,
			want: agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "mimo:compaction"},
		},
		{
			name: "a tool part is no notification kind",
			raw:  `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","type":"tool"}}}`,
		},
		{
			name: "a session error is no notification kind",
			raw:  `{"type":"session.error","properties":{"error":{"name":"APIError"}}}`,
		},
		{name: "bytes that are not an event", raw: `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, mimoProvider{}.Classify(json.RawMessage(tc.raw)))
		})
	}
}

func TestValidateAttachment(t *testing.T) {
	t.Parallel()
	provider := mimoProvider{}
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage, agent.AttachmentKindPDF} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "f"}), "kind %v", kind)
	}
	assert.ErrorContains(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindBinary, Filename: "a.bin"}), "a.bin")
}

// The registry applies the same policy that the plugin states, so a binary
// attachment fails before it reaches the agent.
func TestNormalizeAttachmentsRefusesABinary(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	accepted, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	})
	require.NoError(t, err)
	assert.Len(t, accepted, 3)

	_, err = registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	})
	assert.ErrorContains(t, err, "archive.bin")
}

// The browser writes the answer envelope from the generated OpenCode contract
// table, and a Go struct tag cannot hold a constant. Without this test a rename
// in contracts/opencode-protocol.json would move the browser and leave the
// worker decoding the old name: every answer would fail to decode, and MiMo's
// question tool would wait until the turn ends.
func TestMiMoAnswerTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	result, found := reflect.TypeOf(mimoJSONRPCAnswer{}).FieldByName("Result")
	require.True(t, found)
	for _, tt := range []struct {
		field string
		want  string
	}{
		{"Answers", contracts.OpenCodeAnswerFieldAnswers},
		{"Rejected", contracts.OpenCodeAnswerFieldRejected},
	} {
		field, found := result.Type.FieldByName(tt.field)
		require.True(t, found, "the answer result has no field %s", tt.field)
		assert.Equal(t, tt.want, field.Tag.Get("json"), "the answer result's %s must carry the contract field name", tt.field)
	}
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
