package droid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
				agenttest.AssertPublishedControlKeys(t, droidProvider{}, sink)
				if !tc.mixed {
					var payload struct {
						Details json.RawMessage `json:"details"`
					}
					require.NoError(t, json.Unmarshal(published.Payload, &payload))
					assert.JSONEq(t, tc.details, string(payload.Details))
				}
				answer, err := json.Marshal(droidResponseEnvelope("protected", contracts.DroidPermissionResult{SelectedOption: contracts.DroidPermissionOptionCancel}))
				require.NoError(t, err)
				require.NoError(t, a.SendRawInput(sink.Answer(published.RequestID, answer)), "the protected request remains answerable by the user")
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
	a, sink := newDroidControlTestAgent(t)
	writer := &bufferStdin{}
	a.SetStdinForTest(writer)
	raw, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)
	assert.Error(t, a.SendRawInput(agent.RawInput{Data: raw}))
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)
	answer := sink.Answer(sink.LastPublishedControl().RequestID, raw)
	require.NoError(t, a.SendRawInput(answer))
	assert.Error(t, a.SendRawInput(answer))
	writer.mu.Lock()
	assert.Equal(t, 1, bytes.Count(writer.buf.Bytes(), []byte("\n")))
	writer.mu.Unlock()
}

func TestDroidRefusesUnstatedOrReplacedControlInstance(t *testing.T) {
	a, sink := newDroidControlTestAgent(t)
	writer := &bufferStdin{}
	a.SetStdinForTest(writer)
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)
	published := sink.LastPublishedControl()
	raw, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)

	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw}), agent.ErrControlInstanceUnstated)
	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: "stale-instance"}), agent.ErrControlInstanceReplaced)
	writer.mu.Lock()
	assert.Zero(t, writer.buf.Len())
	writer.mu.Unlock()

	require.NoError(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: published.InstanceID}))
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
	answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(sink.Answer(sink.LastPublishedControl().RequestID, answer)))
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

func TestDroidManualFallbackWaitsForPublication(t *testing.T) {
	a, _ := newDroidControlTestAgent(t)
	a.settings.permissionMode = contracts.DroidModeAutoHigh
	a.SetStdinForTest(agenttest.FailingStdin{})
	entered := make(chan struct{})
	release := make(chan struct{})
	sink := &droidPublicationSink{publish: func() {
		close(entered)
		<-release
	}}
	a.sink = agent.NewProviderServices(sink)
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("manual fallback did not enter PublishControlRequest")
	}

	writer := &agenttest.Stdin{}
	a.SetStdinForTest(writer)
	answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{SelectedOption: contracts.DroidPermissionOptionProceedOnce}))
	require.NoError(t, err)
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.SendRawInput(sink.Answer(sink.LastPublishedControl().RequestID, answer))
	}()

	select {
	case err := <-errCh:
		close(release)
		t.Fatalf("SendRawInput returned before fallback publication completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-errCh)
	assert.Contains(t, writer.String(), `"selectedOption":"proceed_once"`)
}

func TestDroidRegisterControlGuards(t *testing.T) {
	t.Parallel()

	t.Run("empty rpcID is ignored", func(t *testing.T) {
		t.Parallel()
		a, sink := newDroidControlTestAgent(t)
		a.registerControl(&droidPendingControl{requestID: "req-empty-rpc", payload: []byte(`{}`)})
		assert.Zero(t, sink.PublishedControlCount())
		a.Mu.Lock()
		assert.Empty(t, a.controls)
		a.Mu.Unlock()
	})

	t.Run("duplicate requestID keeps original record", func(t *testing.T) {
		t.Parallel()
		a, sink := newDroidControlTestAgent(t)
		first := &droidPendingControl{requestID: "req-dup", rpcID: "rpc-1", instanceID: "inst-1", payload: []byte(`{}`)}
		second := &droidPendingControl{requestID: "req-dup", rpcID: "rpc-2", instanceID: "inst-2", payload: []byte(`{}`)}
		a.registerControl(first)
		a.registerControl(second)
		assert.Equal(t, 1, sink.PublishedControlCount())
		a.Mu.Lock()
		assert.Same(t, first, a.controls["req-dup"])
		a.Mu.Unlock()
	})

	t.Run("stopped agent ignores registration", func(t *testing.T) {
		t.Parallel()
		a, sink := newDroidControlTestAgent(t)
		a.Mu.Lock()
		a.stopped = true
		a.Mu.Unlock()
		a.registerControl(&droidPendingControl{requestID: "req-stopped", rpcID: "rpc-1", payload: []byte(`{}`)})
		assert.Zero(t, sink.PublishedControlCount())
		a.Mu.Lock()
		assert.Empty(t, a.controls)
		a.Mu.Unlock()
	})
}

type droidBlockingStdin struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	mu        sync.Mutex
	writes    int
	partial   bool
}

func (s *droidBlockingStdin) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
	if s.partial {
		return 2, io.ErrClosedPipe
	}
	return len(p), nil
}

func (s *droidBlockingStdin) Close() error { return nil }

func TestDroidFinishControlAnswerClosesPublicationDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		partial bool
	}{
		{name: "auto-bypass succeeds", partial: false},
		{name: "auto-bypass returns ErrDeliveryUncertain", partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newDroidControlTestAgent(t)
			a.settings.permissionMode = contracts.DroidModeAutoHigh
			stdin := &droidBlockingStdin{
				entered: make(chan struct{}),
				release: make(chan struct{}),
				partial: tc.partial,
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(stdin.release) }) }
			defer release()
			a.SetStdinForTest(stdin)

			request := permissionRequest(t, "approval", "Edit")
			a.onRequestPermission(&request)

			ctx := testutil.DeadlineContext(t)
			select {
			case <-stdin.entered:
			case <-ctx.Done():
				t.Fatal("auto-bypass did not begin writing to stdin")
			}

			a.Mu.Lock()
			record := a.controls["droid-perm-approval"]
			a.Mu.Unlock()
			require.NotNil(t, record)
			select {
			case <-record.publicationDone:
				t.Fatal("publicationDone must remain open while auto-bypass write is in flight")
			default:
			}

			answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{
				SelectedOption: contracts.DroidPermissionOptionProceedOnce,
			}))
			require.NoError(t, err)
			errCh := make(chan error, 1)
			go func() {
				errCh <- a.SendRawInput(agent.RawInput{Data: answer, ControlInstanceID: record.instanceID})
			}()

			release()
			select {
			case <-record.publicationDone:
			case <-ctx.Done():
				t.Fatal("finishControlAnswer did not close record.publicationDone")
			}
			select {
			case gotErr := <-errCh:
				assert.ErrorIs(t, gotErr, agent.ErrControlRequestNotPending)
			case <-ctx.Done():
				t.Fatal("concurrent SendRawInput remained blocked after finishControlAnswer")
			}
			require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
			assert.Zero(t, sink.PublishedControlCount())
			if tc.partial {
				// Record stays in a.controls with published=false; a subsequent call must
				// also pass <-record.publicationDone immediately and return ErrControlRequestNotPending.
				assert.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: answer, ControlInstanceID: record.instanceID}), agent.ErrControlRequestNotPending)
			} else {
				a.Mu.Lock()
				assert.Empty(t, a.controls)
				a.Mu.Unlock()
			}
		})
	}
}

func TestDroidManualAnswerUncertainCancelsControlRequestInstance(t *testing.T) {
	t.Parallel()
	a, sink := newDroidControlTestAgent(t)
	a.SetStdinForTest(droidFailedInputWriter{written: 2})
	request := permissionRequest(t, "approval", "Edit")
	a.onRequestPermission(&request)
	published := sink.LastPublishedControl()
	require.NotEmpty(t, published.RequestID)

	answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{
		SelectedOption: contracts.DroidPermissionOptionProceedOnce,
	}))
	require.NoError(t, err)
	err = a.SendRawInput(sink.Answer(published.RequestID, answer))
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Equal(t, []string{published.RequestID}, sink.CanceledControls())
	assert.ErrorIs(t, a.SendRawInput(sink.Answer(published.RequestID, answer)), agent.ErrControlRequestNotPending)
}

func TestDroidSendRawInputWaitInterruptionAndGuards(t *testing.T) {
	t.Parallel()

	t.Run("context canceled while waiting for publicationDone", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		processDone := make(chan struct{})
		defer close(processDone)
		a := &Agent{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID: "droid-ctx-test", Stdin: &bufferStdin{}, Ctx: ctx, Cancel: cancel, ProcessDone: processDone,
			}),
			sink: agent.NewProviderServices(&agenttest.ControlSink{}), sessionID: "native-session",
		}
		record := &droidPendingControl{
			requestID: "req-ctx", rpcID: "rpc-ctx", instanceID: "inst-ctx",
			sessionID: "native-session", published: true, publicationDone: make(chan struct{}),
		}
		a.controls = map[string]*droidPendingControl{"req-ctx": record}

		answer, err := json.Marshal(droidResponseEnvelope("rpc-ctx", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		errCh := make(chan error, 1)
		go func() {
			errCh <- a.SendRawInput(agent.RawInput{Data: answer, ControlInstanceID: "inst-ctx"})
		}()
		cancel()
		select {
		case gotErr := <-errCh:
			assert.ErrorIs(t, gotErr, context.Canceled)
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("SendRawInput did not return on context cancellation")
		}
	})

	t.Run("process exit while waiting for publicationDone", func(t *testing.T) {
		t.Parallel()
		processDone := make(chan struct{})
		a := &Agent{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID: "droid-exit-test", Stdin: &bufferStdin{}, Ctx: t.Context(), ProcessDone: processDone,
			}),
			sink: agent.NewProviderServices(&agenttest.ControlSink{}), sessionID: "native-session",
		}
		record := &droidPendingControl{
			requestID: "req-exit", rpcID: "rpc-exit", instanceID: "inst-exit",
			sessionID: "native-session", published: true, publicationDone: make(chan struct{}),
		}
		a.controls = map[string]*droidPendingControl{"req-exit": record}

		answer, err := json.Marshal(droidResponseEnvelope("rpc-exit", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		errCh := make(chan error, 1)
		go func() {
			errCh <- a.SendRawInput(agent.RawInput{Data: answer, ControlInstanceID: "inst-exit"})
		}()
		close(processDone)
		select {
		case gotErr := <-errCh:
			require.Error(t, gotErr)
			assert.Equal(t, a.ProcessExitError().Error(), gotErr.Error())
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("SendRawInput did not return on process exit")
		}
	})

	t.Run("concurrent SendRawInput while answering is refused", func(t *testing.T) {
		t.Parallel()
		a, sink := newDroidControlTestAgent(t)
		stdin := &droidBlockingStdin{entered: make(chan struct{}), release: make(chan struct{})}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(stdin.release) }) }
		defer release()
		a.SetStdinForTest(stdin)

		request := permissionRequest(t, "approval", "Edit")
		a.onRequestPermission(&request)
		published := sink.LastPublishedControl()

		answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		firstErr := make(chan error, 1)
		go func() {
			firstErr <- a.SendRawInput(sink.Answer(published.RequestID, answer))
		}()
		select {
		case <-stdin.entered:
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("first SendRawInput did not reach stdin write")
		}

		secondErr := a.SendRawInput(sink.Answer(published.RequestID, answer))
		assert.ErrorIs(t, secondErr, agent.ErrControlRequestNotPending)
		release()
		require.NoError(t, <-firstErr)
		stdin.mu.Lock()
		assert.Equal(t, 1, stdin.writes)
		stdin.mu.Unlock()
	})

	t.Run("session mismatch returns ErrInputSessionChanged", func(t *testing.T) {
		t.Parallel()
		a, sink := newDroidControlTestAgent(t)
		writer := &bufferStdin{}
		a.SetStdinForTest(writer)
		request := permissionRequest(t, "approval", "Edit")
		a.onRequestPermission(&request)
		published := sink.LastPublishedControl()

		a.Mu.Lock()
		a.sessionID = "rotated-session"
		a.Mu.Unlock()

		answer, err := json.Marshal(droidResponseEnvelope("approval", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		assert.ErrorIs(t, a.SendRawInput(sink.Answer(published.RequestID, answer)), agent.ErrInputSessionChanged)
		assert.Empty(t, writer.frames())
	})

	t.Run("non-response frame forwards directly to stdin", func(t *testing.T) {
		t.Parallel()
		a, _ := newDroidControlTestAgent(t)
		writer := &bufferStdin{}
		a.SetStdinForTest(writer)

		require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(`{"type":"custom_event","value":1}`)}))
		assert.Equal(t, []string{`{"type":"custom_event","value":1}`}, writer.frames())
	})
}

func TestDroidRouteControlAnswerSingleChildUnstatedAndNonBlocking(t *testing.T) {
	t.Parallel()

	t.Run("single bound child with empty ControlInstanceID returns ErrControlInstanceUnstated", func(t *testing.T) {
		t.Parallel()
		root, child, _, _, rootStdin, childStdin := newBoundChildReplyHarness(t)
		perm := permissionRequest(t, "rpc-single-child", "Edit")
		rawPerm, err := perm.Marshal()
		require.NoError(t, err)
		child.HandleOutput(rawPerm)

		answer, err := json.Marshal(droidResponseEnvelope("rpc-single-child", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		assert.ErrorIs(t, root.SendRawInput(agent.RawInput{Data: answer}), agent.ErrControlInstanceUnstated)
		assert.Empty(t, rootStdin.frames())
		assert.Empty(t, childStdin.frames())
	})

	t.Run("unready and nil-agent child connections do not block routing", func(t *testing.T) {
		t.Parallel()
		root, child, _, childSink, _, childStdin := newBoundChildReplyHarness(t)
		closedReady := make(chan struct{})
		close(closedReady)
		root.childConnMu.Lock()
		root.childConns["unready-child"] = &droidChildConnection{ready: make(chan struct{})}
		root.childConns["nil-agent-child"] = &droidChildConnection{ready: closedReady, agent: nil}
		root.childConnMu.Unlock()

		// Missing request returns immediately without blocking on unready child.
		missing, err := json.Marshal(droidResponseEnvelope("rpc-missing", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		assert.ErrorIs(t, root.SendRawInput(agent.RawInput{Data: missing}), agent.ErrControlRequestNotPending)

		// Routing to the ready child still succeeds while unready and nil-agent slots exist.
		perm := permissionRequest(t, "rpc-ready-child", "Edit")
		rawPerm, err := perm.Marshal()
		require.NoError(t, err)
		child.HandleOutput(rawPerm)
		childReqs := childSink.PublishedControls()
		require.Len(t, childReqs, 1)

		answer, err := json.Marshal(droidResponseEnvelope("rpc-ready-child", contracts.DroidPermissionResult{
			SelectedOption: contracts.DroidPermissionOptionProceedOnce,
		}))
		require.NoError(t, err)
		require.NoError(t, root.SendRawInput(agent.RawInput{
			Data:              answer,
			ControlInstanceID: childReqs[0].InstanceID,
		}))
		assert.Len(t, childStdin.frames(), 1)
	})
}
