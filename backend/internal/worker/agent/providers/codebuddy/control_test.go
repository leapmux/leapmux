package codebuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// permissionFixture is an offline agent that publishes its controls to a ControlSink
// and writes its stdin to a buffer, with a running turn.
func permissionFixture(t *testing.T) (*Agent, *agenttest.ControlSink, *bytes.Buffer) {
	t.Helper()
	controls := &agenttest.ControlSink{}
	a := newOfflineAgent(t, &controls.Sink)
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(controls))
	stdin := &bytes.Buffer{}
	a.SetStdinForTest(agenttest.NopStdin(stdin))
	require.NoError(t, a.SendInput("Run the scripted command.", nil))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"waiting-control","name":"Bash"}]}}`))
	stdin.Reset()
	return a, controls, stdin
}

// canUseTool is CodeBuddy's permission request for one command.
func canUseTool(id string) []byte {
	return []byte(`{"type":"control_request","request_id":"` + id + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"waiting-control","input":{"command":"printf x > out.txt"}}}`)
}

// stdinFrames decodes each stdin line that the agent wrote.
func stdinFrames(t *testing.T, stdin *bytes.Buffer) []map[string]any {
	t.Helper()
	var frames []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(stdin.Bytes()))
	for scanner.Scan() {
		var frame map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &frame))
		frames = append(frames, frame)
	}
	return frames
}

// permissionRefusals lists the request IDs of the refusals that carry an interrupt.
func permissionRefusals(t *testing.T, frames []map[string]any) []string {
	t.Helper()
	var ids []string
	for _, frame := range frames {
		if frame["type"] != frameTypeControlResponse {
			continue
		}
		response, _ := frame["response"].(map[string]any)
		answer, _ := response["response"].(map[string]any)
		if answer["allowed"] == false && answer["interrupt"] == true {
			ids = append(ids, response["request_id"].(string))
		}
	}
	return ids
}

// CodeBuddy stops the native run through a permission refusal that carries interrupt:true.
// A competing control request replaces that refusal and starts a model continuation.
func TestCodebuddyInterruptRefusesEachWaitingPermissionWithoutASecondStop(t *testing.T) {
	t.Parallel()
	a, controls, stdin := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_b"))
	a.HandleOutput(canUseTool("perm_a"))
	require.Len(t, controls.PublishedControls(), 2)

	require.NoError(t, a.Interrupt(agent.StopContext{}))

	frames := stdinFrames(t, stdin)
	require.Len(t, frames, 2, "the native permission interrupt ends the turn without a competing stop")
	assert.Equal(t, []string{"perm_a", "perm_b"}, permissionRefusals(t, frames), "one refusal for each permission, in a stable order")
	assert.Equal(t, []string{"perm_a", "perm_b"}, controls.CanceledControls())

	// A second interrupt finds no waiting permission: each one was refused once.
	stdin.Reset()
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, permissionRefusals(t, stdinFrames(t, stdin)))
	assert.Empty(t, stdinFrames(t, stdin), "a competing stop waits for the native run to end")
}

// The refusal states a reason, which CodeBuddy hands to the model as the result of the
// refused call.
func TestCodebuddyInterruptRefusalStatesItsReason(t *testing.T) {
	t.Parallel()
	a, _, stdin := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_1"))

	require.NoError(t, a.Interrupt(agent.StopContext{}))

	frames := stdinFrames(t, stdin)
	require.NotEmpty(t, frames)
	require.Equal(t, frameTypeControlResponse, frames[0]["type"], "the refusal comes first")
	response, ok := frames[0]["response"].(map[string]any)
	require.True(t, ok, "the refusal carries a response object")
	assert.Equal(t, "success", response["subtype"])
	assert.Equal(t, map[string]any{"allowed": false, "reason": codebuddyInterruptedPermissionReason, "interrupt": true}, response["response"])
}

func TestCodebuddyInterruptKeepsAPermissionWhenItsRefusalFails(t *testing.T) {
	t.Parallel()
	a, controls, _ := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_1"))
	a.SetStdinForTest(agenttest.FailingStdin{})

	require.Error(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, controls.CanceledControls())
	a.mu.Lock()
	_, open := a.openPermissions["perm_1"]
	assert.Empty(t, a.interruptRequests, "a failed refusal marks no stop")
	a.mu.Unlock()
	assert.True(t, open)
}

type permissionRetiringWriter struct {
	retire func()
	output *bytes.Buffer
	err    error
}

func (w permissionRetiringWriter) Write(data []byte) (int, error) {
	w.retire()
	if w.err != nil {
		return 0, w.err
	}
	return w.output.Write(data)
}

func TestCodebuddyFailedRefusalDoesNotRestoreARetiredPermission(t *testing.T) {
	t.Parallel()
	for name, retire := range map[string]func(*Agent){
		"native cancellation": func(a *Agent) {
			a.HandleOutput([]byte(`{"type":"control_cancel_request","request_id":"perm_1"}`))
		},
		"turn end": func(a *Agent) { a.setTurnActive(false) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := permissionFixture(t)
			a.HandleOutput(canUseTool("perm_1"))
			failure := errors.New("the refusal write failed")
			a.SetStdinForTest(agenttest.NopStdin(permissionRetiringWriter{retire: func() { retire(a) }, err: failure}))
			require.ErrorIs(t, a.Interrupt(agent.StopContext{}), failure)
			a.mu.Lock()
			assert.Empty(t, a.openPermissions, "the failed write must not restore a retired permission")
			a.mu.Unlock()
		})
	}
}

func TestCodebuddyRefusalSkipsAPermissionRetiredDuringAnEarlierWrite(t *testing.T) {
	t.Parallel()
	a, controls, stdin := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_a"))
	a.HandleOutput(canUseTool("perm_b"))
	a.SetStdinForTest(agenttest.NopStdin(permissionRetiringWriter{
		retire: func() { a.HandleOutput([]byte(`{"type":"control_cancel_request","request_id":"perm_b"}`)) },
		output: stdin,
	}))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Equal(t, []string{"perm_a"}, permissionRefusals(t, stdinFrames(t, stdin)))
	assert.Equal(t, []string{"perm_b", "perm_a"}, controls.CanceledControls())
}

func TestCodebuddyFailedAnswerKeepsTheNativePermissionOpen(t *testing.T) {
	t.Parallel()
	a, _, _ := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_1"))
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.SendRawInput([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"perm_1","response":{"allowed":true}}}`), agent.StopContext{}))
	a.mu.Lock()
	_, open := a.openPermissions["perm_1"]
	a.mu.Unlock()
	assert.True(t, open, "the failed answer cannot release the native permission wait")
}

// The SDK refusal ends the main run. The later control request also stops
// native child tasks and workflows, before the next input enters the session.
func TestCodebuddyPermissionInterruptSendsGlobalStopAfterTheNativeRunEnds(t *testing.T) {
	t.Parallel()
	a, controls, stdin := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_1"))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Len(t, stdinFrames(t, stdin), 1, "the SDK refusal precedes the global stop")

	a.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Permission denied for tool(s): Bash"]}`))

	frames := stdinFrames(t, stdin)
	require.Len(t, frames, 2, "the global stop follows the native run's result")
	assert.Equal(t, frameTypeControlRequest, frames[1]["type"])
	assert.Equal(t, map[string]any{"subtype": "interrupt", "session_id": "session-1", "reason": "user"}, frames[1]["request"])
	rows := controls.Messages()
	require.NotEmpty(t, rows)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion)
	require.NoError(t, a.SendInput("Run the next turn.", nil))
	a.HandleOutput([]byte(codebuddyResultFinished))
	rows = controls.Messages()
	assert.NotEqual(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion)
}

func TestCodebuddyPermissionInterruptHoldsACompetingStopUntilTheNativeRunEnds(t *testing.T) {
	t.Parallel()
	a, _, stdin := permissionFixture(t)
	a.HandleOutput(canUseTool("perm_1"))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Len(t, stdinFrames(t, stdin), 1, "a competing stop must not replace the SDK refusal")
}

func TestCodebuddyChildPermissionDoesNotDelayTheRootStop(t *testing.T) {
	t.Parallel()
	a, _, stdin := permissionFixture(t)
	a.HandleOutput(bytes.ReplaceAll(canUseTool("child_perm"), []byte(`"tool_use_id":"waiting-control"`), []byte(`"tool_use_id":"child-tool"`)))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	frames := stdinFrames(t, stdin)
	require.Len(t, frames, 1)
	assert.Equal(t, frameTypeControlRequest, frames[0]["type"], "a child permission cannot hold the root's global stop")
}

// CodeBuddy no longer waits on a permission that the reader answered, that the CLI
// withdrew, or that LeapMux could not publish. Only a permission waits on an answer of
// this shape: another control request keeps its own answer.
func TestCodebuddyInterruptRefusesNoPermissionThatNoLongerWaits(t *testing.T) {
	t.Parallel()
	for name, settle := range map[string]func(t *testing.T, a *Agent, controls *agenttest.ControlSink){
		"the reader answered it": func(t *testing.T, a *Agent, _ *agenttest.ControlSink) {
			a.HandleOutput(canUseTool("perm_1"))
			require.NoError(t, a.SendRawInput([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"perm_1","response":{"allowed":true}}}`), agent.StopContext{}))
		},
		"the CLI withdrew it": func(_ *testing.T, a *Agent, _ *agenttest.ControlSink) {
			a.HandleOutput(canUseTool("perm_1"))
			a.HandleOutput([]byte(`{"type":"control_cancel_request","request_id":"perm_1"}`))
		},
		"it could not be published": func(_ *testing.T, a *Agent, controls *agenttest.ControlSink) {
			controls.PublicationError = errors.New("the store is gone")
			a.HandleOutput(canUseTool("perm_1"))
		},
		"it is no permission": func(_ *testing.T, a *Agent, _ *agenttest.ControlSink) {
			a.HandleOutput([]byte(`{"type":"control_request","request_id":"hook_1","request":{"subtype":"hook_callback","callback_id":"c1"}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, controls, stdin := permissionFixture(t)
			settle(t, a, controls)
			stdin.Reset()
			canceled := len(controls.CanceledControls())

			require.NoError(t, a.Interrupt(agent.StopContext{}))

			assert.Empty(t, permissionRefusals(t, stdinFrames(t, stdin)))
			assert.Len(t, controls.CanceledControls(), canceled, "the interrupt withdraws no card")
		})
	}
}
