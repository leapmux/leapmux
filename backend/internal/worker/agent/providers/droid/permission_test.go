package droid

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	raw, err := json.Marshal(map[string]any{
		"toolUses": []any{map[string]any{"toolUse": map[string]any{"type": "tool_use", "id": id, "name": tool, "input": map[string]any{}}, "confirmationType": "tool"}},
		"options":  []any{map[string]string{"label": "Proceed once", "value": contracts.DroidPermissionOptionProceedOnce}, map[string]string{"label": "Cancel", "value": contracts.DroidPermissionOptionCancel}},
	})
	require.NoError(t, err)
	env.Params = raw
	return env
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
