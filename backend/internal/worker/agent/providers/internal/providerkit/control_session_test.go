package providerkit

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionApprovalProcess(t *testing.T, stdin io.WriteCloser) *JSONRPCProcess {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{AgentID: "session-approval", Ctx: ctx, Cancel: cancel, ProcessDone: done, Stdin: stdin})}
	t.Cleanup(func() { close(done); base.Stop() })
	return base
}

func TestSessionControlApprovalRetainsItsExplicitOwner(t *testing.T) {
	output := &agenttest.Stdin{}
	base := sessionApprovalProcess(t, output)
	sink := &agenttest.ControlSink{}
	sink.UpdateSessionID("sink-session")
	payload := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"permission","params":{"sessionId":"native-payload"}}`)
	handle := base.PublishSessionControlRequestWithApproval(sink, "child-session", payload, nil, map[string]any{"approved": true})
	require.NotEmpty(t, handle.RequestID)
	assert.Equal(t, "child-session", sink.LastPublishedControl().AgentSessionID)
	require.True(t, base.ApproveOutstandingControlRequest(sink, handle))
	require.Eventually(t, func() bool { return !base.OutstandingControlForTest(handle.RequestID) }, 30*time.Second, time.Millisecond)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"approved":true}}`, output.String())
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{handle.RequestID}, sink.CanceledControls())
}

func TestUnpublishedSessionApprovalFailureRetainsItsOwnerAndOriginalPayload(t *testing.T) {
	for _, sessionID := range []string{"child-session", ""} {
		t.Run(sessionID, func(t *testing.T) {
			base := sessionApprovalProcess(t, agenttest.FailingStdin{})
			sink := &agenttest.ControlSink{}
			sink.UpdateSessionID("sink-session")
			payload := []byte(` {"jsonrpc":"2.0","id":"007","method":"permission","params":{"future":9007199254740993}} `)
			require.True(t, base.ApproveUnpublishedSessionControlRequest(sink, sessionID, payload, nil, map[string]any{"approved": true}))
			require.Eventually(t, func() bool { return sink.PublishedControlCount() == 1 }, 30*time.Second, time.Millisecond)
			want := sessionID
			if want == "" {
				want = "sink-session"
			}
			assert.Equal(t, want, sink.LastPublishedControl().AgentSessionID)
			assert.Equal(t, payload, sink.LastPublishedControl().Payload)
			assert.True(t, base.OutstandingControlForTest(sink.LastPublishedControl().RequestID))
			assert.Empty(t, sink.CanceledControls())
		})
	}
}
