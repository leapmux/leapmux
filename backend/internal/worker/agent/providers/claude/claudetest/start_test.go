package claudetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestStartEchoAcceptsRawServiceFramesWithoutNativeRequests(t *testing.T) {
	running, err := StartEcho(t.Context(), agent.Options{AgentID: "echo", WorkingDir: t.TempDir()}, agent.NewProviderServices(&agenttest.ControlSink{}))
	require.NoError(t, err)
	t.Cleanup(func() {
		running.Stop()
		_ = running.Wait()
	})
	frame := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"service-request","response":{"behavior":"allow"}}}`)
	require.NoError(t, running.SendRawInput(frame, agent.StopContext{}))
	running.Stop()
	require.ErrorContains(t, running.SendRawInput(frame, agent.StopContext{}), "stopped")
}
