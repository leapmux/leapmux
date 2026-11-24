package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// storedKeyCase is one provider's control request, as its publication stores
// it, for the tests of the Worker's key check.
type storedKeyCase struct {
	provider leapmuxv1.AgentProvider
	// key is the key that the provider publishes payload under.
	key     string
	payload string
	// otherKey is a key that payload does not give.
	otherKey string
	// unkeyed is payload without the part that gives its key.
	unkeyed  string
	decision *leapmuxv1.ControlDecision
}

// storedKeyCases holds a JSON-RPC provider, which never checked its stored
// payload against its key, and each provider that checked it in its own
// resolution before the Worker did.
func storedKeyCases(t *testing.T) map[string]storedKeyCase {
	t.Helper()
	allow := permissionDecisionOf(contracts.ControlAnswerOptionAllow, "")
	gemini := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"call-1","title":"Run"},"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}`
	cline := `{"version":"v1","event":"approval.requested","sessionId":"s1","payload":{"approvalId":"approval_1","sessionId":"s1","agentId":"agent_1","toolCallId":"call_1","toolName":"editor","inputJson":"{}"}}`
	opencode := `{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"shell","resources":["ls"]},"request":{"tool_name":"shell"}}`
	deepseek := `{"type":"waterfall","event":"approval/request","eventId":"ev-1","agentId":"native-root","request":{"callId":"call","toolName":"bash","tool_name":"bash","tool_use_id":"call"}}`
	muse := `{"jsonrpc":"2.0","method":"approval/request","params":{"sessionId":"session","approvalId":"approval","currentRequirementId":{"approvalId":"approval","sourceIndex":0},"availableChoices":[{"choiceId":"allow","label":"Allow once","decision":"approved","scope":"once"},{"choiceId":"deny","label":"Deny once","decision":"denied","scope":"once"}]}}`
	antigravity := `{"cascadeId":"cascade","name":"antigravity_interaction","request":{"input":{"permission":{"resource":{"action":"command","target":"echo"},"actionDescription":"Run echo"}},"tool_name":"run_command","tool_use_id":"call"},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"echo"},"actionDescription":"Run echo"}},"stepIndex":0,"toolCall":{"id":"call","name":"run_command","argumentsJson":""},"trajectoryId":"trajectory"}`
	codewhale := `{"event":{"event":"approval.required","item_id":null,"kind":"approval.required","payload":{"approval_id":"ap1","description":"Execute a shell command in the workspace.","id":"ap1","intent_summary":null,"tool_call_id":"call_1","tool_name":"bash"},"schema_version":1,"seq":2,"thread_id":"thr_1","timestamp":"2026-09-23T18:18:36.078701+00:00","turn_id":"turn_1"},"request":{"tool_name":"bash","tool_use_id":"call_1","input":{}},"request_id":"approval:ap1","type":"control_request"}`
	zcode := `{"type":"control_request","request_id":"req-1","id":7,"method":"interaction/requestPermission","request":{"tool_name":"Bash","input":{"command":"rm -rf build"}},"params":{"requestId":"req-1","sessionId":"sess-1","toolCallId":"call-1","toolName":"Bash","riskLevel":"high","reason":"runs a command","input":{"command":"rm -rf build"},"options":[{"optionId":"allow_once","kind":"allow","name":"Allow once","response":{"decision":"allow","scope":"once"}},{"optionId":"deny","kind":"deny","name":"Deny","response":{"decision":"deny"}}]}}`
	return map[string]storedKeyCase{
		"gemini": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, key: "jsonrpc:7", payload: gemini, otherKey: "jsonrpc:8",
			unkeyed: withoutJSONMember(t, gemini, "id"), decision: permissionDecisionOf("once", ""),
		},
		"cline": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, key: "approval_1", payload: cline, otherKey: "approval_2",
			unkeyed: withoutJSONMember(t, cline, "payload", "approvalId"), decision: allow,
		},
		"opencode": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, key: "opencode:permission:ses_1:per_1", payload: opencode,
			otherKey: "opencode:permission:ses_1:per_2", unkeyed: withoutJSONMember(t, opencode, "data", "id"),
			decision: permissionDecisionOf(contracts.OpenCodeNativePermissionDecisionOnce, ""),
		},
		"deepseek harness": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_DEEPSEEK_HARNESS, key: "ev-1", payload: deepseek, otherKey: "ev-2",
			unkeyed: withoutJSONMember(t, deepseek, "eventId"), decision: permissionDecisionOf("allow", ""),
		},
		"muse": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE, key: `muse:approval:["session","approval",0]`, payload: muse,
			otherKey: `muse:approval:["session","approval",1]`, unkeyed: withoutJSONMember(t, muse, "params", "currentRequirementId"),
			decision: permissionDecisionOf("allow", ""),
		},
		// The step index is part of the key, so a payload of another step under
		// the key of this one disagrees with it, even within one cascade.
		"antigravity": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ANTIGRAVITY_CLI, key: "antigravity:cascade:trajectory:0", payload: antigravity,
			otherKey: "antigravity:cascade:trajectory:1", unkeyed: withoutJSONMember(t, antigravity, "cascadeId"),
			decision: permissionDecisionOf("allow", ""),
		},
		"antigravity, another cascade": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ANTIGRAVITY_CLI, key: "antigravity:cascade:trajectory:0", payload: antigravity,
			otherKey: "antigravity:other:trajectory:0", unkeyed: withoutJSONMember(t, antigravity, "trajectoryId"),
			decision: permissionDecisionOf("allow", ""),
		},
		"codewhale": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, key: "approval:ap1", payload: codewhale, otherKey: "approval:other",
			unkeyed: withoutJSONMember(t, codewhale, "request_id"), decision: allow,
		},
		"zcode": {
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, key: "req-1", payload: zcode, otherKey: "req-OTHER",
			unkeyed: withoutJSONMember(t, zcode, "request_id"), decision: permissionDecisionOf("allow_once", ""),
		},
	}
}

// withoutJSONMember is document without the member at path.
func withoutJSONMember(t *testing.T, document string, path ...string) string {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal([]byte(document), &root))
	object := root
	for _, name := range path[:len(path)-1] {
		next, ok := object[name].(map[string]any)
		require.Truef(t, ok, "%s holds no object at %s", document, name)
		object = next
	}
	_, ok := object[path[len(path)-1]]
	require.Truef(t, ok, "%s holds no member %v", document, path)
	delete(object, path[len(path)-1])
	encoded, err := json.Marshal(root)
	require.NoError(t, err)
	return string(encoded)
}

// The control is the payload under its own key: each provider answers it, so
// a refusal of the other rows comes from their keys alone.
func TestAnswerControl_DeliversAStoredRequestThatAgreesWithItsKey(t *testing.T) {
	t.Parallel()
	for name, tc := range storedKeyCases(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, d, delivered := answerControlFixture(t, tc.provider, tc.key, tc.payload)
			response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
				AgentId: "agent-1", RequestId: tc.key, ClaimToken: "claim-1", Decision: tc.decision,
			})
			require.Empty(t, errs)
			assert.Empty(t, response.GetError())
			assert.Len(t, *delivered, 1)
		})
	}
}

// The Worker writes the key of each stored request from its payload, so a
// payload that gives another key, or none, means a corrupt store or a fault
// in the key's derivation. AnswerControl refuses each answer to such a row
// once, with the shared reason, before any provider reads the row, and the
// request stays for a stop.
func TestAnswerControl_RefusesAStoredRequestThatDisagreesWithItsKey(t *testing.T) {
	t.Parallel()
	for name, tc := range storedKeyCases(t) {
		for row, stored := range map[string]struct{ key, payload string }{
			"a payload that gives another key": {tc.otherKey, tc.payload},
			"a payload that gives no key":      {tc.key, tc.unkeyed},
		} {
			t.Run(name+"/"+row, func(t *testing.T) {
				t.Parallel()
				svc, d, delivered := answerControlFixture(t, tc.provider, stored.key, stored.payload)
				response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
					AgentId: "agent-1", RequestId: stored.key, ClaimToken: "claim-1", Decision: tc.decision,
				})
				require.Empty(t, errs)
				assert.Equal(t, agent.RefusalUnreadableRequest, response.GetError(), "one refusal, with the shared reason")
				assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.GetState())
				assert.Empty(t, *delivered, "nothing reaches the agent")
				requireLookupControlRequest(t, context.Background(), svc.Output, "agent-1", stored.key)
				_, err := svc.Queries.GetControlResponseAnswer(context.Background(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: stored.key, ClaimToken: "claim-1"})
				assert.Error(t, err, "the Worker claims no answer")
			})
		}
	}
}

// The claim path reads the row again, and it checks the row that it claims
// before the provider resolves the answer: a provider can republish under one
// claim token, so the row can differ from the one that AnswerControl read.
func TestProcessControlAnswer_RefusesAStoredRequestThatDisagreesWithItsKey(t *testing.T) {
	t.Parallel()
	for name, tc := range storedKeyCases(t) {
		for row, stored := range map[string]struct{ key, payload string }{
			"a payload that gives another key": {tc.otherKey, tc.payload},
			"a payload that gives no key":      {tc.key, tc.unkeyed},
		} {
			t.Run(name+"/"+row, func(t *testing.T) {
				t.Parallel()
				svc, _, _ := answerControlFixture(t, tc.provider, stored.key, stored.payload)
				dbAgent, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
				require.NoError(t, err)
				// The answer that the provider builds from the same payload, so only
				// the key check stands between it and the agent.
				plugin := svc.Agents.Registry().Plugin(tc.provider)
				input := agent.NewControlRequestInput([]byte(tc.payload))
				answer, err := plugin.AnswerControlRequest(agent.ControlAnswerInput{
					Request: input, RequestID: stored.key, Prompt: agent.ReadControlPrompt(plugin, input), Decision: tc.decision,
				})
				require.NoError(t, err)

				sent, forwarded, responseErr := processControlAnswerForTest(svc, dbAgent, answer.Content, "claim-1")
				require.EqualError(t, responseErr, agent.RefusalUnreadableRequest)
				assert.False(t, forwarded, "nothing reaches the agent")
				assert.Nil(t, sent)
				requireLookupControlRequest(t, context.Background(), svc.Output, "agent-1", stored.key)
			})
		}
	}
}

// processControlAnswer checks the stored request against its key only below
// its two earlier gates, so each gate keeps its own outcome. A gone row states
// that the request is gone, not that its row is unreadable. An answer to a
// retired instance stays silent even when the row that replaced it disagrees
// with its key, because that answer never reaches the row.
func TestProcessControlAnswer_KeepsTheOutcomesOfAGoneRowAndARetiredInstance(t *testing.T) {
	t.Parallel()
	answer := agent.ControlResponseEnvelope("req-1", map[string]any{"behavior": contracts.ControlAnswerBehaviorAllow})

	t.Run("a gone row", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
			ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		sends := 0
		svc.sendControlResponseFn = func(string, agent.RawInput) error { sends++; return nil }
		dbAgent, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
		require.NoError(t, err)

		require.ErrorIs(t, svc.processControlAnswer(dbAgent, workerAnswer{content: answer, claimToken: "claim-1"}), agent.ErrControlRequestNotPending)
		state, errText := deliverControlAnswerForTest(t, svc, "agent-1", "req-1", workerAnswer{content: answer, claimToken: "claim-1"})
		assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, state)
		assert.Equal(t, agent.ErrControlRequestNotPending.Error(), errText, "the reader reads that the request is gone")
		assert.Zero(t, sends)
	})
	t.Run("a retired instance whose replacement disagrees with its key", func(t *testing.T) {
		t.Parallel()
		// The current instance, claim-1, is stored under req-1 and gives req-OTHER.
		svc, _, delivered := answerControlFixture(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "req-1",
			strings.Replace(claudeEditRequest, `"req-1"`, `"req-OTHER"`, 1))
		dbAgent, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
		require.NoError(t, err)

		require.NoError(t, svc.processControlAnswer(dbAgent, workerAnswer{content: answer, claimToken: "retired-claim"}))
		state, errText := deliverControlAnswerForTest(t, svc, "agent-1", "req-1", workerAnswer{content: answer, claimToken: "retired-claim"})
		assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, state)
		assert.Empty(t, errText, "the card of the retired instance is gone, so no failure is left to show")
		assert.Empty(t, *delivered)

		// The current instance's own answer meets the key check.
		state, errText = deliverControlAnswerForTest(t, svc, "agent-1", "req-1", workerAnswer{content: answer, claimToken: "claim-1"})
		assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, state)
		assert.Equal(t, agent.RefusalUnreadableRequest, errText)
		assert.Empty(t, *delivered)
	})
}

// A provider keys its workspace-trust question by the session that the Worker
// stores with it, because the question's payload states no key of its own
// (agent.Provider.ControlRequestKey). So the Worker reads the key in the
// session of the stored row: the question of its own session is answered, and
// a row whose key belongs to another session, or to no session, is refused.
func TestAnswerControl_ReadsTheKeyOfAStoredRequestInItsOwnSession(t *testing.T) {
	t.Parallel()
	question, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          "/work/project",
		contracts.WorkspaceTrustFieldConfiguration: []string{"hooks"},
	})
	require.NoError(t, err)
	// The three providers that ask the question.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	} {
		for name, tc := range map[string]struct {
			rowSession, key string
			answered        bool
		}{
			"the question of its own session": {rowSession: "s1", key: "workspace-trust:s1", answered: true},
			"a key of another session":        {rowSession: "s1", key: "workspace-trust:s2"},
			"a question of no session":        {rowSession: "", key: "workspace-trust:s1"},
		} {
			t.Run(provider.String()+"/"+name, func(t *testing.T) {
				t.Parallel()
				svc, d, _ := setupTestService(t)
				require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
					ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: provider,
				}))
				require.NoError(t, svc.Queries.UpdateAgentSessionID(context.Background(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "s1"}))
				createTestControlRequest(t, context.Background(), svc.Output, testControlRequestParams{
					AgentID: "agent-1", AgentSessionID: tc.rowSession, RequestID: tc.key, ClaimToken: "claim-1", Payload: question,
				})
				var delivered [][]byte
				svc.sendControlResponseFn = func(_ string, answer agent.RawInput) error {
					delivered = append(delivered, append([]byte(nil), answer.Data...))
					return nil
				}

				response, errs := answerControl(t, d, &leapmuxv1.AnswerControlRequest{
					AgentId: "agent-1", RequestId: tc.key, ClaimToken: "claim-1", Decision: permissionDecisionOf("workspace-trust", ""),
				})
				require.Empty(t, errs)
				if !tc.answered {
					assert.Equal(t, agent.RefusalUnreadableRequest, response.GetError())
					assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.GetState())
					assert.Empty(t, delivered)
					return
				}
				assert.Empty(t, response.GetError())
				require.Len(t, delivered, 1)
				assert.Contains(t, string(delivered[0]), `"`+contracts.WorkspaceTrustFieldDecision+`":"`+contracts.WorkspaceTrustDecisionTrust+`"`,
					"the provider receives the trust decision")
			})
		}
	}
}
