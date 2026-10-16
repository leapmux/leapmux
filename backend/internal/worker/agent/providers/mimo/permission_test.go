package mimo

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBypassApprovesPendingAndLaterPermissionsButKeepsQuestions(t *testing.T) {
	a, sink, server := newControlTestAgent(t)
	feed(a, permissionAskedEvent(t, "first", testSessionID, ""), questionAskedEvent(t, "question", testSessionID), planAskedEvent(t, "plan", "absent.md"))
	require.Equal(t, 3, sink.PublishedControlCount())
	result := a.UpdateSettings(map[string]string{contracts.MiMoOptionPermissionPolicy: contracts.MiMoPermissionPolicyBypass})
	require.Equal(t, contracts.MiMoPermissionPolicyBypass, result.ConfirmedOptions()[contracts.MiMoOptionPermissionPolicy])
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"mimo-permission:first"}, sink.CanceledControls())
	require.Len(t, server.requestsTo("POST /permission/first/reply"), 1)
	assert.JSONEq(t, `{"reply":"once"}`, string(server.requestsTo("POST /permission/first/reply")[0].Body))
	feed(a, permissionAskedEvent(t, "later", testSessionID, ""))
	require.Eventually(t, func() bool { return len(server.requestsTo("POST /permission/later/reply")) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, 3, sink.PublishedControlCount())
}

func TestMiMoClaimsAControlBeforeItsHTTPAnswer(t *testing.T) {
	a, _, server := newControlTestAgent(t)
	feed(a, permissionAskedEvent(t, "permission", testSessionID, ""))
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server.handle("POST /permission/permission/reply", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		writeJSON(w, http.StatusOK, `true`)
	})
	done := make(chan error, 1)
	go func() { done <- a.SendRawInput(allowEnvelope("mimo-permission:permission"), agent.StopContext{}) }()
	<-entered
	second := a.SendRawInput(allowEnvelope("mimo-permission:permission"), agent.StopContext{})
	close(release)
	require.NoError(t, <-done)
	assert.Error(t, second)
	assert.Equal(t, int32(1), calls.Load())
}

func TestMiMoKeepsAnAcceptedAnswerAcrossAStaleRequestReplay(t *testing.T) {
	a, sink, server := newControlTestAgent(t)
	request := permissionAskedEvent(t, "permission", testSessionID, "")
	feed(a, request)
	require.NoError(t, a.SendRawInput(allowEnvelope("mimo-permission:permission"), agent.StopContext{}))
	feed(a, request)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Error(t, a.SendRawInput(allowEnvelope("mimo-permission:permission"), agent.StopContext{}))
	assert.Len(t, server.requestsTo("POST /permission/permission/reply"), 1)
}

type instanceControlSink struct {
	agenttest.ControlSink
	mu      sync.Mutex
	first   chan struct{}
	release chan struct{}
	active  map[string]agent.ControlRequest
	count   int
}

func (s *instanceControlSink) PublishControlRequest(request agent.ControlRequest) error {
	s.mu.Lock()
	s.count++
	first := s.count == 1
	s.active[request.RequestID] = request
	s.mu.Unlock()
	if first {
		close(s.first)
		<-s.release
	}
	return nil
}
func (s *instanceControlSink) CancelControlRequest(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}
func (s *instanceControlSink) CancelControlRequestInstance(id, instance string) {
	s.mu.Lock()
	if s.active[id].InstanceID == instance {
		delete(s.active, id)
	}
	s.mu.Unlock()
}

func TestMiMoOldPublicationCannotCancelAReplacement(t *testing.T) {
	a, _, _ := newControlTestAgent(t)
	sink := &instanceControlSink{first: make(chan struct{}), release: make(chan struct{}), active: make(map[string]agent.ControlRequest)}
	a.sink = agent.NewProviderServices(sink)
	old := &mimoControl{kind: controlPermission, nativeID: "permission", sessionID: testSessionID, payload: []byte(`{"request":{"tool_name":"bash","input":{"command":"first"}}}`)}
	next := &mimoControl{kind: controlPermission, nativeID: "permission", sessionID: testSessionID, payload: []byte(`{"request":{"tool_name":"bash","input":{"command":"second"}}}`)}
	done := make(chan struct{})
	go func() { a.publishControl("mimo-permission:permission", old); close(done) }()
	<-sink.first
	a.publishControl("mimo-permission:permission", next)
	close(sink.release)
	<-done
	sink.mu.Lock()
	retained, ok := sink.active["mimo-permission:permission"]
	sink.mu.Unlock()
	require.True(t, ok, "a late old publication must not cancel the new instance")
	assert.Equal(t, next.payload, []byte(retained.Payload))
}

func TestMiMoAutomaticFailurePublishesOneManualFallback(t *testing.T) {
	a, sink, server := newControlTestAgent(t)
	a.permissionPolicy = contracts.MiMoPermissionPolicyBypass
	server.respond("POST /permission/permission/reply", http.StatusInternalServerError, `{}`)
	feed(a, permissionAskedEvent(t, "permission", testSessionID, ""))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Len(t, server.requestsTo("POST /permission/permission/reply"), 1)
	server.respond("POST /permission/permission/reply", http.StatusOK, `true`)
	require.NoError(t, a.SendRawInput(allowEnvelope("mimo-permission:permission"), agent.StopContext{}))
	assert.Len(t, server.requestsTo("POST /permission/permission/reply"), 2)
}
