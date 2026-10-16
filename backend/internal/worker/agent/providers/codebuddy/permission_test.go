package codebuddy

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlPublicationSink struct {
	agenttest.ControlSink
	publish func()
}

func (s *controlPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	err := s.ControlSink.PublishControlRequest(request)
	if s.publish != nil {
		s.publish()
	}
	return err
}

func TestCodebuddyFailedAutomaticWritePublishesOutsideTheWriteLock(t *testing.T) {
	a, _, _ := permissionFixture(t)
	sink := &controlPublicationSink{}
	a.sink = agent.NewProviderServices(sink)
	a.permissionMode = contracts.CodebuddyModeBypassPermissions
	sink.publish = func() {
		if assert.True(t, a.permissionWriteMu.TryLock(), "publication callbacks must run outside the write lock") {
			a.permissionWriteMu.Unlock()
		}
	}
	a.SetStdinForTest(agenttest.FailingStdin{})
	a.HandleOutput(canUseTool("approval"))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, 1, sink.PublishedControlCount())
	writer := &agenttest.Stdin{}
	a.SetStdinForTest(writer)
	answer := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"approval","response":{"allowed":true}}}`)
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Contains(t, writer.String(), `"allowed":true`)
}

func TestCodebuddyModeChangeDuringPublicationResolvesTheSameRequest(t *testing.T) {
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"mode":"bypassPermissions"}`))
	entered, release := make(chan struct{}), make(chan struct{})
	sink := &controlPublicationSink{publish: func() { close(entered); <-release }}
	a.sink = agent.NewProviderServices(sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"waiting-control","name":"Bash"}]}}`))
	done := make(chan struct{})
	go func() { a.HandleOutput(canUseTool("approval")); close(done) }()
	<-entered
	result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModeBypassPermissions})
	assert.Equal(t, contracts.CodebuddyModeBypassPermissions, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	assert.NotContains(t, peer.String(), `"allowed":true`)
	close(release)
	<-done
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Contains(t, peer.String(), `"allowed":true`)
	assert.Equal(t, []string{"approval"}, sink.CanceledControls())
}

func TestCodebuddyBypassPreservesTheExactNativeInput(t *testing.T) {
	a, _, _ := permissionFixture(t)
	a.permissionMode = contracts.CodebuddyModeBypassPermissions
	writer := &agenttest.Stdin{}
	a.SetStdinForTest(writer)
	request := bytes.ReplaceAll(canUseTool("approval"), []byte(`{"command":"printf x > out.txt"}`), []byte(`{"count":9007199254740993,"zero":0,"empty":"","flag":false}`))
	a.HandleOutput(request)
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	for _, part := range []string{`"count":9007199254740993`, `"zero":0`, `"empty":""`, `"flag":false`} {
		assert.Contains(t, writer.String(), part)
	}
}

func TestCodebuddyChildPermissionKeepsAManualAnswerUnderBypass(t *testing.T) {
	a, sink, stdin := permissionFixture(t)
	a.permissionMode = contracts.CodebuddyModeBypassPermissions
	request := bytes.ReplaceAll(canUseTool("child"), []byte(`"tool_use_id":"waiting-control"`), []byte(`"tool_use_id":"child-call","agent_id":"native-child"`))
	a.HandleOutput(request)
	assert.True(t, a.approvalWork.Idle())
	assert.Equal(t, 1, sink.PublishedControlCount())
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	frames := stdinFrames(t, stdin)
	require.Len(t, frames, 1)
	assert.Equal(t, frameTypeControlRequest, frames[0]["type"])
	require.NoError(t, a.SendRawInput([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"child","response":{"allowed":true}}}`), agent.StopContext{}))
}

func TestCodebuddyRetiredTurnCannotApproveDuringPublication(t *testing.T) {
	a, _, _ := permissionFixture(t)
	sink := &controlPublicationSink{}
	a.sink = agent.NewProviderServices(sink)
	var callbacks atomic.Int32
	sink.publish = func() { callbacks.Add(1); a.setTurnActive(false) }
	a.HandleOutput(canUseTool("approval"))
	assert.Equal(t, int32(1), callbacks.Load())
	assert.Equal(t, []string{"approval"}, sink.CanceledControls())
	assert.Empty(t, a.controls)
}

func TestCodebuddyTurnEndWithdrawsPublishedRootPermissionsOnly(t *testing.T) {
	a, sink, _ := permissionFixture(t)
	a.HandleOutput(canUseTool("root"))
	child := bytes.ReplaceAll(canUseTool("child"), []byte(`"tool_use_id":"waiting-control"`), []byte(`"tool_use_id":"child-call","agent_id":"native-child"`))
	a.HandleOutput(child)
	require.Equal(t, 2, sink.PublishedControlCount())
	a.setTurnActive(false)
	assert.Equal(t, []string{"root"}, sink.CanceledControls())
	require.Len(t, a.controls, 1)
	assert.Contains(t, a.controls, "child")
}
