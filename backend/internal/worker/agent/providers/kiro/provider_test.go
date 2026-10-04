package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The worker stores a JSON-RPC request under the key `jsonrpc:<id>`, and the
// browser answers under that key.
const kiroStoredRequestID = "jsonrpc:5"

// kiroRequestPayload is a stored request of one method with the native id 5.
func kiroRequestPayload(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	if params == nil {
		params = map[string]any{"sessionId": kiroTestSession, "toolCallId": "t_1"}
	}
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 5, "method": method, "params": params})
	require.NoError(t, err)
	return payload
}

// permissionParams is a permission request of Kiro's with the given options.
func permissionParams(optionIDs ...string) map[string]any {
	options := make([]any, 0, len(optionIDs))
	for _, id := range optionIDs {
		options = append(options, map[string]any{"optionId": id, "name": id, "kind": "allow_once"})
	}
	return map[string]any{
		"sessionId": kiroTestSession,
		"toolCall":  map[string]any{"toolCallId": "run_command_t_sh", "title": "Running: ls"},
		"options":   options,
	}
}

// withWorkspaceRoot adds the consent of a call in the workspace root to a
// request's params, as Kiro states it for a rule that the workspace can keep.
func withWorkspaceRoot(params map[string]any, root string) map[string]any {
	params["_meta"] = map[string]any{"kiro": map[string]any{
		"consent": map[string]any{"capability": "shell", "resource": "ls", "workspaceRoot": root},
	}}
	return params
}

// selectedReply is the browser's permission reply that selects one option,
// with the given result `_meta`.
func selectedReply(t *testing.T, optionID string, meta map[string]any) []byte {
	t.Helper()
	result := map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
	if meta != nil {
		result["_meta"] = meta
	}
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": kiroStoredRequestID, "result": result})
	require.NoError(t, err)
	return data
}

func resolveKiro(t *testing.T, payload json.RawMessage, response []byte) agent.ControlResponseResolution {
	t.Helper()
	return kiroProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       kiroStoredRequestID,
		RequestPayload:  payload,
		ResponseContent: response,
	})
}

// kiroEveryAlwaysOption is a permission request that offers each of Kiro's
// four options, as Kiro states them for a rule that can persist.
func kiroEveryAlwaysOption() map[string]any {
	return permissionParams("accept", contracts.KiroPermissionOptionAlwaysAccept, "reject", contracts.KiroPermissionOptionAlwaysReject)
}

// Kiro reads the consent scope of the reply for its always-reject as for its
// always-accept, and keeps a deny rule at that scope.
func TestKiroScopedAlwaysOptionBecomesKirosConsentScope(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, withWorkspaceRoot(kiroEveryAlwaysOption(), "/w"))
	for _, tc := range []struct {
		optionID string
		kiroID   string
		scope    string
	}{
		{optionID: contracts.KiroScopedPermissionOptionAlwaysAcceptWorkspace, kiroID: contracts.KiroPermissionOptionAlwaysAccept, scope: contracts.KiroConsentScopeWorkspace},
		{optionID: contracts.KiroScopedPermissionOptionAlwaysAcceptUser, kiroID: contracts.KiroPermissionOptionAlwaysAccept, scope: contracts.KiroConsentScopeUser},
		{optionID: contracts.KiroScopedPermissionOptionAlwaysRejectWorkspace, kiroID: contracts.KiroPermissionOptionAlwaysReject, scope: contracts.KiroConsentScopeWorkspace},
		{optionID: contracts.KiroScopedPermissionOptionAlwaysRejectUser, kiroID: contracts.KiroPermissionOptionAlwaysReject, scope: contracts.KiroConsentScopeUser},
	} {
		t.Run(tc.optionID, func(t *testing.T) {
			t.Parallel()
			result := resolveKiro(t, payload, selectedReply(t, tc.optionID, nil))

			require.False(t, result.Withhold)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"`+tc.kiroID+`"},"_meta":{"kiro":{"consent":{"scope":"`+tc.scope+`"}}}}}`, string(result.Content))
		})
	}
}

func TestKiroScopedAlwaysAcceptKeepsTheOtherMetadata(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, permissionParams("accept", contracts.KiroPermissionOptionAlwaysAccept))
	reply := selectedReply(t, contracts.KiroScopedPermissionOptionAlwaysAcceptUser, map[string]any{
		"kiro":  map[string]any{"editedCommand": "ls -la", "consent": map[string]any{"note": "kept"}},
		"other": true,
	})

	result := resolveKiro(t, payload, reply)

	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"always-accept"},"_meta":{"kiro":{"editedCommand":"ls -la","consent":{"note":"kept","scope":"user"}},"other":true}}}`, string(result.Content))
}

func TestKiroScopedAlwaysOptionIsWithheldWhenKiroDidNotOfferItsOwn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		offered  []string
		optionID string
	}{
		// Kiro offers always-accept only for an implicit ask whose rule can
		// persist, and always-reject only for an ask whose rule can persist.
		{name: "an allow with no always-accept", offered: []string{"accept", "reject", contracts.KiroPermissionOptionAlwaysReject}, optionID: contracts.KiroScopedPermissionOptionAlwaysAcceptWorkspace},
		{name: "a deny with no always-reject", offered: []string{"accept", contracts.KiroPermissionOptionAlwaysAccept, "reject"}, optionID: contracts.KiroScopedPermissionOptionAlwaysRejectUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := kiroRequestPayload(t, kiroPermissionMethod, withWorkspaceRoot(permissionParams(tc.offered...), "/w"))

			assert.True(t, resolveKiro(t, payload, selectedReply(t, tc.optionID, nil)).Withhold)
		})
	}
}

// A rule for the workspace needs the workspace root, which the request states
// in its consent. The browser offers the workspace scope only with a root, and
// the worker holds the same rule, so a client that sends the scope anyway gets
// no reply that Kiro would refuse or apply to no workspace.
func TestKiroWorkspaceAlwaysOptionIsWithheldWithNoWorkspaceRoot(t *testing.T) {
	t.Parallel()
	for name, params := range map[string]map[string]any{
		"no consent":    kiroEveryAlwaysOption(),
		"an empty root": withWorkspaceRoot(kiroEveryAlwaysOption(), " "),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload := kiroRequestPayload(t, kiroPermissionMethod, params)

			for _, optionID := range []string{contracts.KiroScopedPermissionOptionAlwaysAcceptWorkspace, contracts.KiroScopedPermissionOptionAlwaysRejectWorkspace} {
				assert.True(t, resolveKiro(t, payload, selectedReply(t, optionID, nil)).Withhold, optionID)
			}
			for _, optionID := range []string{contracts.KiroScopedPermissionOptionAlwaysAcceptUser, contracts.KiroScopedPermissionOptionAlwaysRejectUser} {
				assert.False(t, resolveKiro(t, payload, selectedReply(t, optionID, nil)).Withhold,
					"the user scope needs no workspace: %s", optionID)
			}
		})
	}
}

func TestKiroScopedAlwaysAcceptWithUnreadableMetadataIsWithheld(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, permissionParams(contracts.KiroPermissionOptionAlwaysAccept))
	for name, meta := range map[string]map[string]any{
		"kiro is not an object":    {"kiro": "x"},
		"consent is not an object": {"kiro": map[string]any{"consent": []any{1}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, resolveKiro(t, payload, selectedReply(t, contracts.KiroScopedPermissionOptionAlwaysAcceptUser, meta)).Withhold)
		})
	}
}

// A reply whose `_meta` is not an object has no room for the consent scope.
// The rewrite cannot keep what the reply carried, so Kiro gets no reply.
func TestKiroScopedAlwaysAcceptWithAMetadataThatIsNotAnObjectIsWithheld(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, permissionParams(contracts.KiroPermissionOptionAlwaysAccept))
	reply := []byte(`{"jsonrpc":"2.0","id":"` + kiroStoredRequestID + `","result":{"outcome":{"outcome":"selected","optionId":"` +
		contracts.KiroScopedPermissionOptionAlwaysAcceptUser + `"},"_meta":"x"}}`)

	assert.True(t, resolveKiro(t, payload, reply).Withhold)
}

// A stored request whose options LeapMux cannot read offers no option that a
// scoped answer could stand for, so the answer is withheld.
func TestKiroScopedAnswerToAnUnreadableRequestIsWithheld(t *testing.T) {
	t.Parallel()
	params := permissionParams()
	params["options"] = "always-accept"
	payload := kiroRequestPayload(t, kiroPermissionMethod, params)

	assert.True(t, resolveKiro(t, payload, selectedReply(t, contracts.KiroScopedPermissionOptionAlwaysAcceptUser, nil)).Withhold)
}

// The rewrite reads the outcome of a result. A reply that carries none, or an
// outcome that is not an object, passes as the shared resolution built it.
func TestKiroPermissionReplyWithoutAnOutcomePassesUnchanged(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, kiroEveryAlwaysOption())
	for name, tc := range map[string]struct {
		reply string
		want  string
	}{
		"an error reply": {
			reply: `{"jsonrpc":"2.0","id":"jsonrpc:5","error":{"code":-32000,"message":"no"}}`,
			want:  `{"jsonrpc":"2.0","id":5,"error":{"code":-32000,"message":"no"}}`,
		},
		"an outcome that is not an object": {
			reply: `{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":"selected"}}`,
			want:  `{"jsonrpc":"2.0","id":5,"result":{"outcome":"selected"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := resolveKiro(t, payload, []byte(tc.reply))
			require.False(t, result.Withhold)
			assert.JSONEq(t, tc.want, string(result.Content))
		})
	}
}

func TestKiroPermissionRepliesOfKirosOwnOptionsPassUnchanged(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, kiroEveryAlwaysOption())
	for _, optionID := range []string{"accept", contracts.KiroPermissionOptionAlwaysAccept, "reject", contracts.KiroPermissionOptionAlwaysReject} {
		t.Run(optionID, func(t *testing.T) {
			t.Parallel()
			result := resolveKiro(t, payload, selectedReply(t, optionID, nil))
			require.False(t, result.Withhold)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"`+optionID+`"}}}`, string(result.Content))
		})
	}

	// A rejection carries its reason in Kiro's own metadata.
	reply := selectedReply(t, "reject", map[string]any{"kiro": map[string]any{"rejectionReason": "Use rg instead."}})
	result := resolveKiro(t, payload, reply)
	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"reject"},"_meta":{"kiro":{"rejectionReason":"Use rg instead."}}}}`, string(result.Content))
}

func TestKiroCancelledPermissionPassesUnchanged(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, kiroPermissionMethod, permissionParams("accept"))

	result := resolveKiro(t, payload, []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":{"outcome":"cancelled"}}}`))

	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"cancelled"}}}`, string(result.Content))
}

func TestKiroElicitationRepliesWithMCPsAnswer(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, contracts.KiroMethodMcpElicitation, nil)
	for _, tc := range []struct {
		action string
		want   string
	}{
		{action: "accept", want: `{"action":"accept","content":{"choice":"red"}}`},
		{action: "decline", want: `{"action":"decline"}`},
		{action: "cancel", want: `{"action":"cancel"}`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			response, err := json.Marshal(map[string]any{
				"type": "control_response",
				"response": map[string]any{"request_id": kiroStoredRequestID, "response": map[string]any{
					"action": tc.action, "content": map[string]any{"choice": "red"},
				}},
			})
			require.NoError(t, err)

			result := resolveKiro(t, payload, response)

			require.False(t, result.Withhold)
			var reply struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &reply))
			assert.Equal(t, "5", string(reply.ID), "the reply restores the native id")
			assert.JSONEq(t, tc.want, string(reply.Result))
		})
	}
}

func TestKiroElicitationWithholdsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, contracts.KiroMethodMcpElicitation, nil)
	for name, response := range map[string]map[string]any{
		"unknown action":  {"request_id": kiroStoredRequestID, "response": map[string]any{"action": "maybe"}},
		"another request": {"request_id": "jsonrpc:6", "response": map[string]any{"action": "accept"}},
		"a persist scope": {"request_id": kiroStoredRequestID, "response": map[string]any{"action": "accept", "_meta": map[string]any{"persist": "always"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(map[string]any{"type": "control_response", "response": response})
			require.NoError(t, err)
			assert.True(t, resolveKiro(t, payload, data).Withhold)
		})
	}
}

func TestKiroQuestionReplyPassesUnchanged(t *testing.T) {
	t.Parallel()
	payload := kiroRequestPayload(t, contracts.KiroMethodUserInput, nil)
	// The browser writes Kiro's own reply for a question.
	answer := []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"action":"answered","answer":"Postgres [PostGIS]"}}`)

	result := resolveKiro(t, payload, answer)

	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"action":"answered","answer":"Postgres [PostGIS]"}}`, string(result.Content))
}

func TestKiroResolveWithoutAStoredRequestForwards(t *testing.T) {
	t.Parallel()
	response := selectedReply(t, "accept", nil)
	result := kiroProvider{}.ResolveControlResponse(agent.ControlResponseContext{ResponseContent: response})
	assert.False(t, result.Withhold)
	assert.Equal(t, response, result.Content)
}

func TestKiroValidateAttachmentRefusesOnlyABinary(t *testing.T) {
	t.Parallel()
	p := kiroProvider{}
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage, agent.AttachmentKindPDF} {
		assert.NoError(t, p.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "f"}), "kind %v", kind)
	}
	err := p.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindBinary, Filename: "app.zip"})
	require.Error(t, err)
	assert.EqualError(t, err, "attachment app.zip is binary, and Kiro does not support binary attachments", "the reader reads the product name as the product spells it")
}

// The registry classifies each attachment and applies the same policy that the
// plugin states, so a binary file fails before it reaches Kiro.
func TestKiroNormalizeAttachmentsRefusesABinary(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	accepted, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO, []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	})
	require.NoError(t, err)
	assert.Len(t, accepted, 3)

	_, err = registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO, []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	})
	assert.ErrorContains(t, err, "archive.bin")
}

func TestKiroRecognizesTheACPInterrupt(t *testing.T) {
	t.Parallel()
	assert.True(t, kiroProvider{}.IsInterrupt(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`))
	assert.False(t, kiroProvider{}.IsInterrupt(`{"jsonrpc":"2.0","method":"_session/steer"}`))
}

func TestKiroResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
