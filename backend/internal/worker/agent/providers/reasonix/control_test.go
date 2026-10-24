package reasonix

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionBypassRequiresNativeToolMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		allow        bool
	}{
		{"ordinary", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":false}}}}`, true},
		{"question", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"ask","fresh":false}}}}`, false},
		{"plan", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"exit_plan_mode","fresh":false}}}}`, false},
		{"another tool", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"ask_again","fresh":false}}}}`, true},
		{"fresh approval", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":true}}}}`, false},
		{"missing freshness", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash"}}}}`, false},
		{"null freshness", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":null}}}}`, false},
		{"malformed freshness", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":"false"}}}}`, false},
		{"write access", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":false,"kind":"write_access"}}}}`, false},
		{"unknown kind", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":false,"kind":"future_protected_request"}}}}`, false},
		{"malformed kind", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash","fresh":false,"kind":42}}}}`, false},
		{"sandbox escape", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"sandbox_escape","fresh":false}}}}`, false},
		{"managed configuration", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"config_write","fresh":false}}}}`, false},
		{"remember", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"remember","fresh":false}}}}`, false},
		{"forget", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"forget","fresh":false}}}}`, false},
		{"foreign metadata", `{"toolCall":{"_meta":{"other":{"tool":"bash"}}}}`, false},
		{"empty tool", `{"toolCall":{"_meta":{"reasonix.io":{"tool":""}}}}`, false},
		{"blank tool", `{"toolCall":{"_meta":{"reasonix.io":{"tool":" ","fresh":false}}}}`, false},
		{"missing", `{}`, false},
		{"wrong tool type", `{"toolCall":{"_meta":{"reasonix.io":{"tool":false}}}}`, false},
		{"malformed", `[`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, permissionAllowsBypass(&providerkit.ParsedLine{Params: []byte(tc.params)}))
		})
	}
}

func TestReasonixProtectedApprovalRemainsManualInYolo(t *testing.T) {
	t.Parallel()
	for _, initial := range []string{contracts.ReasonixApprovalAsk, contracts.ReasonixApprovalYolo} {
		t.Run(initial, func(t *testing.T) {
			t.Parallel()
			config := func(value string) []acp.ConfigOption {
				return []acp.ConfigOption{{ID: contracts.ReasonixConfigToolApproval, Type: "select", CurrentValue: value, Options: []acp.ConfigOptionValue{
					{Value: contracts.ReasonixApprovalAsk, Name: "Ask"},
					{Value: contracts.ReasonixApprovalAuto, Name: "Auto"},
					{Value: contracts.ReasonixApprovalYolo, Name: "Yolo"},
				}}}
			}
			a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
				func() *Agent { return &Agent{} },
				func(a *Agent) *acp.Base { return &a.Base },
				func(request agenttest.RecordedRequest) agenttest.RPCReply {
					if request.Method == "test/barrier" {
						return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
					}
					assert.Equal(t, acp.MethodSessionSetConfigOption, request.Method)
					assert.Equal(t, contracts.ReasonixConfigToolApproval, request.Params["configId"])
					value, ok := request.Params["value"].(string)
					assert.True(t, ok)
					body, err := json.Marshal(map[string]any{"configOptions": config(value)})
					assert.NoError(t, err)
					return agenttest.RPCReply{Result: body}
				},
			)
			sink := &agenttest.ControlSink{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			a.HooksForTest().PermissionAllowsBypass = permissionAllowsBypass
			a.HooksForTest().WithBypassPermission = func(claim func()) {
				a.WithOptionValue(contracts.ReasonixConfigToolApproval, contracts.ReasonixApprovalYolo, claim)
			}
			a.Mu.Lock()
			a.ApplyOptionGroupsLockedForTest(config(initial))
			a.Mu.Unlock()
			publish := func(id int, metadata, option string) {
				frame := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"_meta":{"reasonix.io":%s}},"options":[{"optionId":%q,"kind":"allow_once"}]}}`, id, metadata, option))
				a.PublishSessionControlRequest(providerkit.ParseLine(frame), map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
			}
			publish(101, `{"tool":"bash","fresh":true,"kind":"write_access"}`, "reasonix_write_once")
			publish(102, `{"tool":"bash","fresh":false}`, "allow_once")
			if initial == contracts.ReasonixApprovalAsk {
				result := a.UpdateSettings(map[string]string{contracts.ReasonixConfigToolApproval: contracts.ReasonixApprovalYolo})
				require.True(t, result.AppliedLive)
				assert.Equal(t, contracts.ReasonixApprovalYolo, result.ConfirmedOptions()[contracts.ReasonixConfigToolApproval])
			}
			// Approval frames enter the stdin queue before publication or UpdateSettings returns.
			// The peer's later reply proves it observed every earlier automatic answer.
			_, err := a.SendRequest("test/barrier", json.RawMessage(`{}`), 30*time.Second)
			require.NoError(t, err)
			var written []string
			for _, request := range requests() {
				written = append(written, request.Raw)
			}
			replies := agenttest.JSONRPCResultsByID(t, strings.Join(written, "\n"))
			assert.NotContains(t, replies, "101", "native write-access decisions require a person even in Yolo")
			assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"allow_once"}}`, replies["102"], "ordinary pending tools still receive their native approval")
			wantPublished := 1
			if initial == contracts.ReasonixApprovalAsk {
				wantPublished = 2
			}
			assert.Equal(t, wantPublished, sink.PublishedControlCount())
			if controls := sink.PublishedControls(); len(controls) > 0 {
				assert.True(t, a.OutstandingControlForTest(controls[0].RequestID), "the protected control remains answerable")
			}
		})
	}
}
