package deepseekharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serveControlReplies(t *testing.T, a *Agent, reply func(http.ResponseWriter, map[string]any)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		reply(w, request)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
	a.clientID = "client"
}

func publishPermission(t *testing.T, a *Agent, id string) {
	t.Helper()
	request := json.RawMessage(`{"toolName":"bash","callId":"call"}`)
	raw, err := json.Marshal(map[string]any{"type": "waterfall", "event": contracts.DeepseekHarnessControlEventApproval, "eventId": id, "agentId": "native-root", "request": request})
	require.NoError(t, err)
	require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventApproval, id, "native-root", request, raw))
}

func controlAnswer(id string) []byte {
	return []byte(`{"response":{"request_id":"` + id + `","response":{"behavior":"allow"}}}`)
}

func TestNativeControlAcceptsOnlyOneConcurrentAnswer(t *testing.T) {
	a := newOfflineAgent(t, &agenttest.Sink{})
	entered, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	serveControlReplies(t, a, func(w http.ResponseWriter, request map[string]any) {
		if count.Add(1) == 1 {
			close(entered)
			<-release
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request["rpcId"], "result": map[string]any{"ok": true}}))
	})
	publishPermission(t, a, "approval")
	done := make(chan error, 1)
	go func() { done <- a.answerControl(controlAnswer("approval")) }()
	<-entered
	second := a.answerControl(controlAnswer("approval"))
	close(release)
	require.NoError(t, <-done)
	assert.Error(t, second)
	assert.Equal(t, int32(1), count.Load())
	assert.Error(t, a.answerControl(controlAnswer("approval")))
}

func TestAutomaticNativeRefusalKeepsAManualFallbackWithoutRetry(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	a.permissions = contracts.DeepseekHarnessPermissionPresetDangerFullAccess
	var count atomic.Int32
	serveControlReplies(t, a, func(w http.ResponseWriter, request map[string]any) {
		count.Add(1)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request["rpcId"], "result": map[string]any{"ok": false, "error": map[string]any{"code": "REFUSED", "message": "The approval was refused."}}}))
	})
	publishPermission(t, a, "approval")
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, int32(1), count.Load())
	assert.Error(t, a.answerControl(controlAnswer("approval")))
	assert.Equal(t, int32(2), count.Load(), "a definite refusal permits a manual retry")
	assert.Empty(t, sink.CanceledControls())
}

func TestUnconfirmedNativeAnswerCannotBeSentAgain(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	var count atomic.Int32
	serveControlReplies(t, a, func(w http.ResponseWriter, _ map[string]any) {
		count.Add(1)
		_, err := w.Write([]byte(`{"type":"wrong-receipt"}`))
		assert.NoError(t, err)
	})
	publishPermission(t, a, "approval")
	assert.ErrorIs(t, a.answerControl(controlAnswer("approval")), agent.ErrDeliveryUncertain)
	assert.Equal(t, []string{"approval"}, sink.CanceledControls(), "an uncertain delivery cannot leave an answerable card")
	assert.Error(t, a.answerControl(controlAnswer("approval")))
	assert.Equal(t, int32(1), count.Load())
}

type publicationBarrier struct {
	agenttest.ControlSink
	entered chan struct{}
	release chan struct{}
}

func (s *publicationBarrier) PublishControlRequest(request agent.ControlRequest) error {
	err := s.ControlSink.PublishControlRequest(request)
	close(s.entered)
	<-s.release
	return err
}

func TestCancellationDuringPublicationRetiresTheVisibleCard(t *testing.T) {
	for range 50 {
		sink := &publicationBarrier{entered: make(chan struct{}), release: make(chan struct{})}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		published := make(chan struct{})
		go func() { publishPermission(t, a, "approval"); close(published) }()
		<-sink.entered
		canceled := make(chan error, 1)
		go func() { canceled <- a.handleRemoteEvent([]byte(`{"type":"cancel","eventId":"approval"}`)) }()
		close(sink.release)
		require.NoError(t, <-canceled)
		<-published
		assert.Equal(t, []string{"approval"}, sink.CanceledControls())
	}
}

func TestAutomaticApprovalRequiresAnOrdinaryToolIdentity(t *testing.T) {
	for _, tool := range []string{"", contracts.DeepseekHarnessToolAskUserQuestion, contracts.DeepseekHarnessToolExitPlanMode} {
		t.Run(tool, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
			a.permissions = contracts.DeepseekHarnessPermissionPresetDangerFullAccess
			calls := serveNativeSettings(t, a)
			a.clientID = "client"
			request, _ := json.Marshal(map[string]any{"toolName": tool, "callId": "call"})
			raw, _ := json.Marshal(map[string]any{"type": "waterfall", "event": contracts.DeepseekHarnessControlEventApproval, "eventId": "approval", "agentId": "native-root", "request": json.RawMessage(request)})
			require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventApproval, "approval", "native-root", request, raw))
			require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
			assert.Empty(t, calls.methods())
			assert.Equal(t, 1, sink.PublishedControlCount())
		})
	}
}
