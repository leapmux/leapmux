//go:build unix

package copilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestNativeCopilotControlResponseRequiresAReceipt(t *testing.T) {
	for _, outcome := range []string{"timeout", "missing-receipt", "protocol-error"} {
		t.Run(outcome, func(t *testing.T) {
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_CONTROL_OUTCOME=" + outcome},
			})
			sink := &agenttest.ControlSink{}
			provider, err := startNativeCopilot(t.Context(), agent.Options{
				AgentID: "native-control-receipt", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), APITimeout: time.Second,
			}, agent.NewProviderServices(sink))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			a := provider.(*Agent)
			a.HandleOutput([]byte(fmt.Sprintf(`{"method":"session.event","params":{"sessionId":%q,"event":{"type":"permission.requested","data":{"requestId":"request"}}}}`, a.currentNativeSessionID())))
			identifier := sink.LastPublishedControl().RequestID
			response := []byte(fmt.Sprintf(`{"response":{"request_id":%q,"response":{"kind":"approve-once"}}}`, identifier))
			err = a.SendRawInput(response)
			require.Error(t, err)
			require.Equal(t, outcome != "protocol-error", errors.Is(err, agent.ErrDeliveryUncertain))
		})
	}
}

func TestNativeCopilotRefusedControlResponseRemainsPending(t *testing.T) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: []string{"LEAPMUX_TEST_COPILOT_CONTROL_REJECT=1"},
	})
	sink := &agenttest.ControlSink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-control-refusal", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	a.HandleOutput([]byte(fmt.Sprintf(`{"method":"session.event","params":{"sessionId":%q,"event":{"type":"permission.requested","data":{"requestId":"request"}}}}`, a.currentNativeSessionID())))
	require.Equal(t, 1, sink.PublishedControlCount())
	identifier := sink.LastPublishedControl().RequestID
	response := []byte(fmt.Sprintf(`{"response":{"request_id":%q,"response":{"kind":"approve-once"}}}`, identifier))
	require.ErrorContains(t, a.SendRawInput(response), "did not accept")
	a.controlMu.Lock()
	pending := a.controls[identifier]
	a.controlMu.Unlock()
	require.NotNil(t, pending)
	a.stateMu.Lock()
	a.sessionID = "another-session"
	a.stateMu.Unlock()
	require.ErrorContains(t, a.SendRawInput(response), "previous session")
	a.stateMu.Lock()
	a.sessionID = pending.sessionID
	a.stateMu.Unlock()
}

func TestNativeCopilotControlResponseDelivery(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	sink := &agenttest.ControlSink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-controls", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	cases := []struct {
		kind, method, field, answer string
	}{
		{"permission", "permissions.handlePendingPermissionRequest", "result", `{"kind":"approve-once"}`},
		{"user_input", "ui.handlePendingUserInput", "response", `{"answer":"","wasFreeform":false}`},
		{"exit_plan_mode", "ui.handlePendingExitPlanMode", "response", `{"approved":false,"autoApproveEdits":false,"selectedAction":"exit_only","feedback":""}`},
		{"elicitation", "ui.handlePendingElicitation", "result", `{"action":"accept","content":{"count":0,"enabled":false,"text":"","large":9007199254740993}}`},
	}
	for index, tc := range cases {
		requestID := fmt.Sprintf("native-request-%d", index)
		frame := fmt.Sprintf(`{"method":"session.event","params":{"sessionId":%q,"event":{"type":%q,"data":{"requestId":%q}}}}`, a.currentNativeSessionID(), tc.kind+".requested", requestID)
		a.HandleOutput([]byte(frame))
		require.Equal(t, index+1, sink.PublishedControlCount())
		identifier := sink.LastPublishedControl().RequestID
		response := []byte(fmt.Sprintf(`{"response":{"request_id":%q,"response":%s}}`, identifier, tc.answer))
		require.NoError(t, a.SendRawInput(response))
		require.ErrorContains(t, a.SendRawInput(response), "no longer pending")
	}
	raw, err := os.ReadFile(requestsPath)
	require.NoError(t, err)
	for index, tc := range cases {
		matched := false
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
			var request struct {
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal(line, &request))
			if request.Method == "session."+tc.method {
				matched = true
				require.JSONEq(t, fmt.Sprintf(`"native-request-%d"`, index), string(request.Params["requestId"]))
				require.Equal(t, tc.answer, string(request.Params[tc.field]))
			}
		}
		require.True(t, matched, tc.method)
	}
}

// deliverCopilotControl answers one pending permission request and reports the value
// the runtime received.
func deliverCopilotControl(t *testing.T, a *Agent, sink *agenttest.ControlSink, answer string) string {
	t.Helper()
	identifier := sink.LastPublishedControl().RequestID
	require.NoError(t, a.SendRawInput(fmt.Appendf(nil, `{"response":{"request_id":%q,"response":%s}}`, identifier, answer)))
	raw, err := a.SendRequest("probe.lastControlValue", json.RawMessage(`{}`), time.Second)
	require.NoError(t, err)
	var probe struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(raw, &probe))
	return probe.Value
}

func startCopilotForControls(t *testing.T, env ...string) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: env,
	})
	sink := &agenttest.ControlSink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-control-location", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 2 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	announceCopilotPermission(t, a, "request")
	return a, sink
}

// announceCopilotPermission delivers one native permission request to the agent.
func announceCopilotPermission(t *testing.T, a *Agent, requestID string) {
	t.Helper()
	a.HandleOutput(fmt.Appendf(nil,
		`{"method":"session.event","params":{"sessionId":%q,"event":{"type":"permission.requested","data":{"requestId":%q,"permissionRequest":{"kind":"read","path":"/project/main.go"}}}}}`,
		a.currentNativeSessionID(), requestID))
}

// Only the runtime can turn a working directory into its own location key, so the
// agent resolves it at delivery and leaves every other value in the answer alone.
func TestNativeCopilotProjectApprovalCarriesTheResolvedLocation(t *testing.T) {
	a, sink := startCopilotForControls(t)
	delivered := deliverCopilotControl(t, a, sink,
		`{"kind":"approve-for-location","approval":{"kind":"read"},"unknown":9007199254740993,"empty":""}`)
	require.Contains(t, delivered, `"locationKey":"location-`+a.opts.WorkingDir+`"`)
	require.Contains(t, delivered, `"unknown":9007199254740993`, "the untouched fields keep their exact bytes")
	require.Contains(t, delivered, `"empty":""`)
}

// An answer that already names its location, and every other decision, reaches the
// runtime byte for byte.
func TestNativeCopilotOtherApprovalsPassThroughUnchanged(t *testing.T) {
	a, sink := startCopilotForControls(t)
	for index, answer := range []string{
		`{"kind":"approve-once"}`,
		`{"kind":"approve-for-session","approval":{"kind":"read"}}`,
		`{"kind":"approve-for-location","approval":{"kind":"read"},"locationKey":"already-resolved"}`,
		`{"kind":"reject","feedback":""}`,
	} {
		// Each answered request is completed by the runtime, so the next decision
		// needs a request of its own.
		announceCopilotPermission(t, a, fmt.Sprintf("request-%d", index))
		require.JSONEq(t, answer, deliverCopilotControl(t, a, sink, answer), answer)
	}
}

// A directory the runtime resolves to no location refuses the delivery. Sending an
// approval with no location would address it to nothing.
func TestNativeCopilotProjectApprovalNeedsALocation(t *testing.T) {
	a, sink := startCopilotForControls(t, "LEAPMUX_TEST_COPILOT_NO_LOCATION=1")
	identifier := sink.LastPublishedControl().RequestID
	err := a.SendRawInput(fmt.Appendf(nil,
		`{"response":{"request_id":%q,"response":{"kind":"approve-for-location","approval":{"kind":"read"}}}}`, identifier))
	require.ErrorContains(t, err, "no permission location")
}
