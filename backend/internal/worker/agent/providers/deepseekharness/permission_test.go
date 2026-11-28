package deepseekharness

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
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
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
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
	answer := sink.Answer("approval", controlAnswer("approval"))
	done := make(chan error, 1)
	go func() { done <- a.answerControl(answer) }()
	<-entered
	second := a.answerControl(answer)
	close(release)
	require.NoError(t, <-done)
	assert.Error(t, second)
	assert.Equal(t, int32(1), count.Load())
	assert.Error(t, a.answerControl(answer))
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
	assert.Error(t, a.answerControl(sink.Answer("approval", controlAnswer("approval"))))
	assert.Equal(t, int32(2), count.Load(), "a definite refusal permits a manual retry")
	assert.Empty(t, sink.CanceledControls())
}

func TestDeepSeekHarnessRefusesUnstatedOrReplacedControlInstance(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	var count atomic.Int32
	serveControlReplies(t, a, func(w http.ResponseWriter, request map[string]any) {
		count.Add(1)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request["rpcId"], "result": map[string]any{"ok": true}}))
	})
	publishPermission(t, a, "approval")
	published := sink.LastPublishedControl()
	raw := controlAnswer("approval")

	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw}), agent.ErrControlInstanceUnstated)
	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: "stale-instance"}), agent.ErrControlInstanceReplaced)
	assert.Zero(t, count.Load())

	require.NoError(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: published.InstanceID}))
	assert.Equal(t, int32(1), count.Load())
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
	answer := sink.Answer("approval", controlAnswer("approval"))
	assert.ErrorIs(t, a.answerControl(answer), agent.ErrDeliveryUncertain)
	assert.Equal(t, []string{"approval"}, sink.CanceledControls(), "an uncertain delivery cannot leave an answerable card")
	assert.Error(t, a.answerControl(answer))
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

type instanceCancelRecord struct {
	requestID  string
	instanceID string
}

type instanceCancelSink struct {
	agenttest.ControlSink
	mu               sync.Mutex
	unscopedCancels  []string
	instanceCancels  []instanceCancelRecord
	publishGate      chan struct{}
	publishEntered   chan struct{}
	publishEnterOnce sync.Once
}

func (s *instanceCancelSink) PublishControlRequest(req agent.ControlRequest) error {
	err := s.ControlSink.PublishControlRequest(req)
	if s.publishEntered != nil {
		s.publishEnterOnce.Do(func() { close(s.publishEntered) })
	}
	if s.publishGate != nil {
		<-s.publishGate
	}
	return err
}

func (s *instanceCancelSink) CancelControlRequest(requestID string) {
	s.mu.Lock()
	s.unscopedCancels = append(s.unscopedCancels, requestID)
	s.mu.Unlock()
	s.ControlSink.CancelControlRequest(requestID)
}

func (s *instanceCancelSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.mu.Lock()
	s.instanceCancels = append(s.instanceCancels, instanceCancelRecord{requestID: requestID, instanceID: instanceID})
	s.mu.Unlock()
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func TestControlWithdrawalsUseInstanceScopedCancellation(t *testing.T) {
	t.Parallel()

	t.Run("remote cancel event uses CancelControlRequestInstance", func(t *testing.T) {
		t.Parallel()
		sink := &instanceCancelSink{}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		publishPermission(t, a, "ev-cancel")
		pub := sink.LastPublishedControl()
		require.NoError(t, a.handleRemoteEvent([]byte(`{"type":"cancel","eventId":"ev-cancel"}`)))
		assert.Empty(t, sink.unscopedCancels)
		assert.Equal(t, []instanceCancelRecord{{requestID: "ev-cancel", instanceID: pub.InstanceID}}, sink.instanceCancels)
		assert.Zero(t, a.unboundControls.Load())
	})

	t.Run("stream failure uses CancelControlRequestInstance", func(t *testing.T) {
		t.Parallel()
		sink := &instanceCancelSink{}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		done := make(chan struct{})
		close(done)
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "deepseek-fail-cancel", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard),
		})
		publishPermission(t, a, "ev-fail")
		pub := sink.LastPublishedControl()
		a.reportStreamFailure(errors.New("dropped"))
		<-a.failureStopDone
		assert.Empty(t, sink.unscopedCancels)
		assert.Equal(t, []instanceCancelRecord{{requestID: "ev-fail", instanceID: pub.InstanceID}}, sink.instanceCancels)
		assert.Zero(t, a.unboundControls.Load())
	})

	t.Run("automatic bypass approval uses CancelControlRequestInstance", func(t *testing.T) {
		t.Parallel()
		sink := &instanceCancelSink{}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		serveControlReplies(t, a, func(w http.ResponseWriter, req map[string]any) {
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": req["rpcId"], "result": map[string]any{"ok": true}}))
		})
		publishPermission(t, a, "ev-auto")
		pub := sink.LastPublishedControl()
		assert.Equal(t, int32(1), a.unboundControls.Load())
		a.Mu.Lock()
		a.permissions = contracts.DeepseekHarnessPermissionPresetDangerFullAccess
		a.Mu.Unlock()
		a.queuePermissionApprovals()
		require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
		assert.Empty(t, sink.unscopedCancels)
		assert.Equal(t, []instanceCancelRecord{{requestID: "ev-auto", instanceID: pub.InstanceID}}, sink.instanceCancels)
		assert.Zero(t, a.unboundControls.Load())
	})

	t.Run("uncertain delivery uses CancelControlRequestInstance", func(t *testing.T) {
		t.Parallel()
		sink := &instanceCancelSink{}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		serveControlReplies(t, a, func(w http.ResponseWriter, _ map[string]any) {
			_, _ = w.Write([]byte(`{"type":"wrong-receipt"}`))
		})
		publishPermission(t, a, "ev-uncertain")
		pub := sink.LastPublishedControl()
		assert.ErrorIs(t, a.answerControl(sink.Answer("ev-uncertain", controlAnswer("ev-uncertain"))), agent.ErrDeliveryUncertain)
		assert.Empty(t, sink.unscopedCancels)
		assert.Equal(t, []instanceCancelRecord{{requestID: "ev-uncertain", instanceID: pub.InstanceID}}, sink.instanceCancels)
	})

	t.Run("cancel during publication uses CancelControlRequestInstance", func(t *testing.T) {
		t.Parallel()
		sink := &instanceCancelSink{publishEntered: make(chan struct{}), publishGate: make(chan struct{})}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		done := make(chan struct{})
		go func() { publishPermission(t, a, "ev-race"); close(done) }()
		<-sink.publishEntered
		require.NoError(t, a.handleRemoteEvent([]byte(`{"type":"cancel","eventId":"ev-race"}`)))
		close(sink.publishGate)
		<-done
		pub := sink.LastPublishedControl()
		assert.Empty(t, sink.unscopedCancels)
		assert.Equal(t, []instanceCancelRecord{{requestID: "ev-race", instanceID: pub.InstanceID}}, sink.instanceCancels)
	})
}

func TestAnswerControlRejectsNullQuestionAnswers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	var called atomic.Int32
	serveControlReplies(t, a, func(w http.ResponseWriter, req map[string]any) {
		called.Add(1)
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": req["rpcId"], "result": map[string]any{"ok": true}}))
	})
	request := json.RawMessage(`{"questions":[{"id":"q1","question":"Pick one?","options":[{"label":"A"}]}]}`)
	raw, err := json.Marshal(map[string]any{"type": "waterfall", "event": contracts.DeepseekHarnessControlEventUserQuestions, "eventId": "q-null", "agentId": "native-root", "request": request})
	require.NoError(t, err)
	require.NoError(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventUserQuestions, "q-null", "native-root", request, raw))

	for _, rawAnswer := range []string{
		`{"response":{"request_id":"q-null","response":{"behavior":"allow","answers":null}}}`,
		`{"response":{"request_id":"q-null","response":{"behavior":"allow"}}}`,
		`{"response":{"request_id":"q-null","response":{"behavior":"allow","answers":"not-an-array"}}}`,
	} {
		err = a.answerControl(sink.Answer("q-null", []byte(rawAnswer)))
		require.Error(t, err, rawAnswer)
	}
	assert.Zero(t, called.Load())
}

type failingPublishControlSink struct {
	agenttest.ControlSink
	cause error
}

func (s *failingPublishControlSink) PublishControlRequest(agent.ControlRequest) error {
	return s.cause
}

func TestPublishControlFailureClearsUnboundControlCounter(t *testing.T) {
	t.Parallel()
	cause := errors.New("publish refused")
	sink := &failingPublishControlSink{cause: cause}
	a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
	request := json.RawMessage(`{"toolName":"bash","callId":"call-fail"}`)
	raw, err := json.Marshal(map[string]any{"type": "waterfall", "event": contracts.DeepseekHarnessControlEventApproval, "eventId": "ev-pub-fail", "agentId": "native-root", "request": request})
	require.NoError(t, err)

	require.ErrorIs(t, a.publishNativeControl(contracts.DeepseekHarnessControlEventApproval, "ev-pub-fail", "native-root", request, raw), cause)
	assert.Zero(t, a.unboundControls.Load())
	a.Mu.Lock()
	assert.Empty(t, a.controls)
	a.Mu.Unlock()
}
