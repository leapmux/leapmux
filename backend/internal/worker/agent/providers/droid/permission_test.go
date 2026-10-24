package droid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionRequest(t *testing.T, id, tool string) droidEnvelope {
	t.Helper()
	env := newDroidEnvelope(droidTypeRequest)
	env.ID, env.Method = id, droidMethodRequestPermission
	kind := "edit"
	if tool == contracts.DroidToolExitSpecMode {
		kind = "exit_spec_mode"
	}
	raw, err := json.Marshal(map[string]any{
		"toolUses": []any{map[string]any{"toolUse": map[string]any{"type": "tool_use", "id": id, "name": tool, "input": map[string]any{}}, "confirmationType": kind, "details": map[string]string{"type": kind}}},
		"options":  []any{map[string]string{"label": "Proceed once", "value": contracts.DroidPermissionOptionProceedOnce}, map[string]string{"label": "Cancel", "value": contracts.DroidPermissionOptionCancel}},
	})
	require.NoError(t, err)
	env.Params = raw
	return env
}

func TestDroidBypassPreservesProtectedConfirmations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		details string
		mixed   bool
	}{
		{name: "shield", kind: "droid_shield_violation", details: `{"type":"droid_shield_violation","command":"git commit","reason":"Review the protected operation."}`},
		{name: "sandbox", kind: "sandbox_violation", details: `{"type":"sandbox_violation","target":"/private/path","operationType":"write"}`},
		{name: "missing kind", details: `{"type":"exec"}`},
		{name: "unknown kind", kind: "future_kind", details: `{"type":"future_kind"}`},
		{name: "missing details", kind: "exec", details: `null`},
		{name: "malformed details", kind: "exec", details: `"invalid"`},
		{name: "mismatched details", kind: "exec", details: `{"type":"droid_shield_violation"}`},
		{name: "shield later in batch", kind: "droid_shield_violation", details: `{"type":"droid_shield_violation"}`, mixed: true},
		{name: "empty script", kind: "script", details: `{"type":"script","calls":[]}`},
		{name: "script protected call", kind: "script", details: `{"type":"script","calls":[{"toolName":"Execute","confirmation":{"type":"droid_shield_violation"}}]}`},
		{name: "script protected second call", kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"}},{"toolName":"Execute","confirmation":{"type":"sandbox_violation"}}]}`},
		{name: "script always asks", kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"},"alwaysAsk":true}]}`},
		{name: "script malformed policy", kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"},"alwaysAsk":"false"}]}`},
		{name: "script null policy", kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"},"alwaysAsk":null}]}`},
		{name: "script missing tool", kind: "script", details: `{"type":"script","calls":[{"confirmation":{"type":"edit"}}]}`},
		{name: "script question", kind: "script", details: `{"type":"script","calls":[{"toolName":"AskUser","confirmation":{"type":"edit"}}]}`},
		{name: "script plan", kind: "script", details: `{"type":"script","calls":[{"toolName":"ExitSpecMode","confirmation":{"type":"exec"}}]}`},
		{name: "nested script", kind: "script", details: `{"type":"script","calls":[{"toolName":"Script","confirmation":{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"}}]}}]}`},
	} {
		for _, alreadyHigh := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/already-high=%t", tc.name, alreadyHigh), func(t *testing.T) {
				a, sink := newDroidControlTestAgent(t)
				a.settings.permissionMode = contracts.DroidModeDefault
				if alreadyHigh {
					a.settings.permissionMode = contracts.DroidModeAutoHigh
				}
				writer := &agenttest.Stdin{}
				a.SetStdinForTest(writer)
				request := permissionRequest(t, "protected", "Execute")
				var params map[string]any
				require.NoError(t, json.Unmarshal(request.Params, &params))
				uses := params["toolUses"].([]any)
				use := uses[0].(map[string]any)
				use["confirmationType"] = tc.kind
				use["details"] = json.RawMessage(tc.details)
				if tc.mixed {
					first := map[string]any{"toolUse": map[string]any{"type": "tool_use", "id": "protected", "name": "Edit", "input": map[string]any{}}, "confirmationType": "edit", "details": map[string]string{"type": "edit"}}
					use["toolUse"].(map[string]any)["id"] = "protected-extra"
					params["toolUses"] = []any{first, use}
				}
				var err error
				request.Params, err = json.Marshal(params)
				require.NoError(t, err)
				a.onRequestPermission(&request)
				ordinary := permissionRequest(t, "ordinary", "Edit")
				a.onRequestPermission(&ordinary)
				if !alreadyHigh {
					a.onSettingsUpdated([]byte(`{"type":"settings_updated","settings":{"interactionMode":"auto","autonomyLevel":"high"}}`))
				}
				require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
				frames := strings.Split(strings.TrimSpace(writer.String()), "\n")
				require.Len(t, frames, 1, "only the ordinary tool receives an automatic response")
				var reply droidEnvelope
				require.NoError(t, json.Unmarshal([]byte(frames[0]), &reply))
				assert.Equal(t, "ordinary", reply.ID)
				assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(reply.Result))
				assert.NotContains(t, sink.CanceledControls(), "droid-perm-protected")
				var published *agenttest.ControlRequestRecord
				for _, control := range sink.PublishedControls() {
					if control.RequestID == "droid-perm-protected" {
						published = &control
					}
				}
				require.NotNil(t, published)
				if !tc.mixed {
					var payload struct {
						Details json.RawMessage `json:"details"`
					}
					require.NoError(t, json.Unmarshal(published.Payload, &payload))
					assert.JSONEq(t, tc.details, string(payload.Details))
				}
				answer, err := json.Marshal(droidResponseEnvelope("protected", droidPermissionResult{SelectedOption: contracts.DroidPermissionOptionCancel}))
				require.NoError(t, err)
				require.NoError(t, a.SendRawInput(answer, agent.StopContext{}), "the protected request remains answerable by the user")
			})
		}
	}
}

func TestDroidBypassAllowsOrdinaryConfirmations(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		details string
	}{
		{kind: "edit", details: `{"type":"edit"}`},
		{kind: "exec", details: `{"type":"exec"}`},
		{kind: "create", details: `{"type":"create"}`},
		{kind: "apply_patch", details: `{"type":"apply_patch"}`},
		{kind: "mcp_tool", details: `{"type":"mcp_tool"}`},
		{kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"}}]}`},
		{kind: "script", details: `{"type":"script","calls":[{"toolName":"Edit","confirmation":{"type":"edit"},"alwaysAsk":false},{"toolName":"Execute","confirmation":{"type":"exec"}}]}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a, sink := newDroidControlTestAgent(t)
			a.settings.permissionMode = contracts.DroidModeAutoHigh
			writer := &agenttest.Stdin{}
			a.SetStdinForTest(writer)
			request := permissionRequest(t, "ordinary", "Execute")
			var params map[string]any
			require.NoError(t, json.Unmarshal(request.Params, &params))
			use := params["toolUses"].([]any)[0].(map[string]any)
			use["confirmationType"], use["details"] = tc.kind, json.RawMessage(tc.details)
			var err error
			request.Params, err = json.Marshal(params)
			require.NoError(t, err)
			a.onRequestPermission(&request)
			require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
			assert.Empty(t, sink.PublishedControls())
			var reply droidEnvelope
			require.NoError(t, json.Unmarshal([]byte(writer.String()), &reply))
			assert.Equal(t, "ordinary", reply.ID)
			assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(reply.Result))
		})
	}
}

func TestDroidBypassApprovesPendingAndLaterToolsButKeepsPlans(t *testing.T) {
	a, sink := newDroidControlTestAgent(t)
	a.settings.permissionMode = contracts.DroidModeDefault
	writer := &droidRequestWriter{requests: make(chan droidEnvelope, 8)}
	a.SetStdinForTest(writer)
	first := permissionRequest(t, "first", "Edit")
	a.onRequestPermission(&first)
	plan := permissionRequest(t, "plan", contracts.DroidToolExitSpecMode)
	a.onRequestPermission(&plan)
	done := make(chan agent.SettingsApplyResult, 1)
	go func() {
		done <- a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.DroidModeAutoHigh})
	}()
	var settings droidEnvelope
	select {
	case settings = <-writer.requests:
	case <-time.After(30 * time.Second):
		t.Fatal("Droid received no settings request")
	}
	require.Equal(t, droidMethodUpdateSessionSettings, settings.Method)
	event, err := json.Marshal(map[string]any{"type": "settings_updated", "requestId": settings.ID, "settings": map[string]string{"interactionMode": "auto", "autonomyLevel": "high"}})
	require.NoError(t, err)
	a.onSettingsUpdated(event)
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID, reply.Result = settings.ID, json.RawMessage(`{}`)
	a.settleReply(reply)
	select {
	case result := <-done:
		assert.Equal(t, contracts.DroidModeAutoHigh, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	case <-time.After(30 * time.Second):
		t.Fatal("Droid did not confirm its settings")
	}
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"droid-perm-first"}, sink.CanceledControls())
	select {
	case answer := <-writer.requests:
		assert.Equal(t, "first", answer.ID)
		assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(answer.Result))
	case <-time.After(30 * time.Second):
		t.Fatal("Droid received no pending approval")
	}
	later := permissionRequest(t, "later", "Edit")
	a.onRequestPermission(&later)
	select {
	case answer := <-writer.requests:
		assert.Equal(t, "later", answer.ID)
	case <-time.After(30 * time.Second):
		t.Fatal("Droid received no later approval")
	}
	assert.Equal(t, 2, sink.PublishedControlCount())
}

func TestDroidRejectsUnknownAndRepeatedControlAnswers(t *testing.T) {
	a, _ := newDroidControlTestAgent(t)
	writer := &bufferStdin{}
	a.SetStdinForTest(writer)
	raw, err := json.Marshal(droidResponseEnvelope("approval", droidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)
	assert.Error(t, a.SendRawInput(raw, agent.StopContext{}))
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)
	require.NoError(t, a.SendRawInput(raw, agent.StopContext{}))
	assert.Error(t, a.SendRawInput(raw, agent.StopContext{}))
	writer.mu.Lock()
	assert.Equal(t, 1, bytes.Count(writer.buf.Bytes(), []byte("\n")))
	writer.mu.Unlock()
}

func TestDroidFailedAutomaticWriteKeepsAManualFallback(t *testing.T) {
	a, sink := newDroidControlTestAgent(t)
	a.settings.permissionMode = contracts.DroidModeAutoHigh
	a.SetStdinForTest(agenttest.FailingStdin{})
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, 1, sink.PublishedControlCount())
	writer := &agenttest.Stdin{}
	a.SetStdinForTest(writer)
	answer, err := json.Marshal(droidResponseEnvelope("approval", droidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Contains(t, writer.String(), `"selectedOption":"proceed_once"`)
}

func TestDroidBypassRequiresAnOrdinaryRootToolAndAnOfferedOnceChoice(t *testing.T) {
	for _, kind := range []string{"no choice", "plan in batch", "child session", "missing tool"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := newDroidControlTestAgent(t)
			a.settings.permissionMode = contracts.DroidModeAutoHigh
			writer := &agenttest.Stdin{}
			a.SetStdinForTest(writer)
			request := permissionRequest(t, "approval", "Edit")
			var params map[string]any
			require.NoError(t, json.Unmarshal(request.Params, &params))
			switch kind {
			case "no choice":
				params["options"] = []any{}
			case "plan in batch":
				params["toolUses"] = append(params["toolUses"].([]any), map[string]any{"toolUse": map[string]any{"id": "plan", "name": contracts.DroidToolExitSpecMode}})
			case "child session":
				params["associatedSessionIds"] = []string{"child-session"}
			case "missing tool":
				params["toolUses"].([]any)[0].(map[string]any)["toolUse"].(map[string]any)["name"] = ""
			}
			encoded, err := json.Marshal(params)
			require.NoError(t, err)
			request.Params = encoded
			a.onRequestPermission(&request)
			assert.True(t, a.approvalWork.Idle())
			assert.Equal(t, 1, sink.PublishedControlCount())
			assert.Empty(t, writer.String())
		})
	}
}

type droidPublicationSink struct {
	agenttest.ControlSink
	publish func()
}

func (s *droidPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	err := s.ControlSink.PublishControlRequest(request)
	s.publish()
	return err
}

func TestDroidPublicationRechecksModeAndTurnLifetime(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprint(retire), func(t *testing.T) {
			a, _ := newDroidControlTestAgent(t)
			writer := &agenttest.Stdin{}
			a.SetStdinForTest(writer)
			a.startTurn()
			sink := &droidPublicationSink{publish: func() {
				if retire {
					a.endTurn()
				} else {
					a.onSettingsUpdated([]byte(`{"type":"settings_updated","settings":{"interactionMode":"auto","autonomyLevel":"high"}}`))
				}
			}}
			a.sink = agent.NewProviderServices(sink)
			request := permissionRequest(t, "approval", "Edit")
			a.onRequestPermission(&request)
			require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
			assert.Equal(t, []string{"droid-perm-approval"}, sink.CanceledControls())
			if retire {
				assert.Empty(t, writer.String())
			} else {
				assert.Contains(t, writer.String(), `"selectedOption":"proceed_once"`)
			}
		})
	}
}
