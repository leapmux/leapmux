package copilot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

// newCopilotControlAgent builds an agent whose connection is present but cannot write.
//
// Every real agent adopts its connection before the reader delivers the first frame, so
// a test that drives the reader needs one too. The failing writer keeps the agent
// offline: a request the dispatch starts fails at the write rather than waiting for an
// answer that never arrives.
func newCopilotControlAgent(sink agentapi.ProviderServices) *Agent {
	return &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.FailingStdin{}}),
		}},
		sink: sink, sessionID: "session",
	}
}

func TestNativeCopilotControlChangeWhileResponseWaits(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replacement), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pending := &copilotPendingControl{
					spec: copilotControlSpecs[1], sessionID: "session", nativeRequestID: "question", instanceID: "instance", ready: make(chan struct{}),
				}
				agent := &Agent{
					copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.FailingStdin{}})}},
					sessionID:         "session", controls: map[string]*copilotPendingControl{"control": pending},
				}
				result := make(chan error, 1)
				go func() {
					result <- agent.SendRawInput(agentapi.RawInput{Data: []byte(`{"response":{"request_id":"control","response":{"answer":"old answer","wasFreeform":true}}}`), ControlInstanceID: "instance"})
				}()
				synctest.Wait()
				agent.controlMu.Lock()
				delete(agent.controls, "control")
				if replacement {
					agent.controls["control"] = &copilotPendingControl{nativeRequestID: "question", sessionID: "session"}
				}
				agent.controlMu.Unlock()
				close(pending.ready)
				require.ErrorContains(t, <-result, "no longer pending")
			})
		})
	}
}

// copilotInstanceSink records the instance that each request ID shows now, as
// the Worker's store does. A cancellation of an instance removes the request
// only while it shows that instance. The gate holds the first publication after
// the sink stored it, as a publication whose return the provider sees late. A
// first publication that fails with failFirst stores nothing.
type copilotInstanceSink struct {
	agenttest.ControlSink
	mu        sync.Mutex
	active    map[string]string
	calls     int
	entered   chan struct{}
	release   chan struct{}
	failFirst error
}

func newCopilotInstanceSink() *copilotInstanceSink {
	return &copilotInstanceSink{active: make(map[string]string), entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (s *copilotInstanceSink) PublishControlRequest(request agentapi.ControlRequest) error {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	fail := first && s.failFirst != nil
	if !fail {
		s.active[request.RequestID] = request.InstanceID
	}
	s.mu.Unlock()
	if first {
		s.entered <- struct{}{}
		<-s.release
	}
	if fail {
		return s.failFirst
	}
	return s.ControlSink.PublishControlRequest(request)
}

func (s *copilotInstanceSink) CancelControlRequest(requestID string) {
	s.mu.Lock()
	delete(s.active, requestID)
	s.mu.Unlock()
	s.ControlSink.CancelControlRequest(requestID)
}

func (s *copilotInstanceSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.mu.Lock()
	if s.active[requestID] == instanceID {
		delete(s.active, requestID)
	}
	s.mu.Unlock()
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func (s *copilotInstanceSink) activeInstance(requestID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.active[requestID]
	return instance, ok
}

// copilotCanceledInstance is one withdrawal of a card: the request, and the
// instance of the request that the withdrawal states.
type copilotCanceledInstance struct{ requestID, instanceID string }

// copilotCancelLog records the instance that each withdrawal states. The
// Worker withdraws only the card of the instance that a withdrawal states
// (CancelControlRequestInstance), and ignores a withdrawal that states none,
// so a withdrawal of another instance leaves on the screen the card that it
// was meant to remove. A withdrawal by the request ID alone states no
// instance.
type copilotCancelLog struct {
	agenttest.ControlSink
	mu       sync.Mutex
	canceled []copilotCanceledInstance
}

func (s *copilotCancelLog) CancelControlRequest(requestID string) {
	s.record(requestID, "")
	s.ControlSink.CancelControlRequest(requestID)
}

func (s *copilotCancelLog) CancelControlRequestInstance(requestID, instanceID string) {
	s.record(requestID, instanceID)
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func (s *copilotCancelLog) record(requestID, instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceled = append(s.canceled, copilotCanceledInstance{requestID: requestID, instanceID: instanceID})
}

func (s *copilotCancelLog) canceledInstances() []copilotCanceledInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]copilotCanceledInstance(nil), s.canceled...)
}

// canceledInstance is the withdrawal of the card of the published request.
func canceledInstance(request agenttest.ControlRequestRecord) copilotCanceledInstance {
	return copilotCanceledInstance{requestID: request.RequestID, instanceID: request.InstanceID}
}

// Each card that the agent withdraws is the card of one instance: the
// replaced instance when an announcement with other data replaces a request,
// and the current instance when Copilot completes the request.
func TestNativeCopilotWithdrawsEachCardByItsInstance(t *testing.T) {
	sink := &copilotCancelLog{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	ask := func(question string) {
		a.HandleOutput(fmt.Appendf(nil, `{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question","question":%q}}}}`, question))
	}
	ask("Choose a color.")
	require.Equal(t, 1, sink.PublishedControlCount())
	replaced := sink.LastPublishedControl()
	ask("Choose a different color.")
	require.Equal(t, 2, sink.PublishedControlCount())
	current := sink.LastPublishedControl()
	require.Equal(t, replaced.RequestID, current.RequestID)
	require.NotEqual(t, replaced.InstanceID, current.InstanceID)
	require.Equal(t, []copilotCanceledInstance{canceledInstance(replaced)}, sink.canceledInstances(),
		"the replacement withdraws the card of the instance that it replaced")

	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.completed","data":{"requestId":"question"}}}}`))
	require.Equal(t, []copilotCanceledInstance{canceledInstance(replaced), canceledInstance(current)}, sink.canceledInstances(),
		"the completion withdraws the card of the current instance")
	require.Empty(t, a.controls)
}

// The end of a session withdraws the card of each waiting request by its
// instance.
func TestNativeCopilotClearWithdrawsTheCardOfEachInstance(t *testing.T) {
	sink := &copilotCancelLog{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question"}}}}`))
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"elicitation.requested","data":{"requestId":"form"}}}}`))
	published := sink.PublishedControls()
	require.Len(t, published, 2)

	a.clearNativeControls()
	require.ElementsMatch(t, []copilotCanceledInstance{canceledInstance(published[0]), canceledInstance(published[1])}, sink.canceledInstances())
	require.Empty(t, a.controls)
}

func awaitCopilotSink(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the publication did not reach the sink")
	}
}

// A publication that finds its request replaced when it returns withdraws
// its own instance. It must not withdraw the replacement's card, which the
// request ID alone would, because the replacement's publication stored its
// own instance under the same request ID meanwhile.
func TestNativeCopilotLatePublicationCannotCancelItsReplacement(t *testing.T) {
	sink := newCopilotInstanceSink()
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	first := &copilotPendingControl{spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "permission", instanceID: "first", payload: []byte(`{"first":true}`), ready: make(chan struct{})}
	second := &copilotPendingControl{spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "permission", instanceID: "second", payload: []byte(`{"second":true}`), ready: make(chan struct{})}
	a.controls = map[string]*copilotPendingControl{"control": first}
	done := make(chan error, 1)
	go func() { done <- a.publishNativeControl("control", first) }()
	awaitCopilotSink(t, sink.entered)
	a.controlMu.Lock()
	a.controls["control"] = second
	a.controlMu.Unlock()
	require.NoError(t, a.publishNativeControl("control", second))
	close(sink.release)
	require.NoError(t, <-done)
	instance, ok := sink.activeInstance("control")
	require.True(t, ok, "the late publication withdrew the card of its replacement")
	require.Equal(t, "second", instance)
}

// recordingFailingStdin records each frame that the agent writes, and fails the
// write without delivering a byte, so every request fails at once.
type recordingFailingStdin struct {
	mu     sync.Mutex
	frames []string
}

func (s *recordingFailingStdin) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.frames = append(s.frames, string(p))
	s.mu.Unlock()
	return 0, errors.New("the pipe closed")
}

func (*recordingFailingStdin) Close() error { return nil }

func (s *recordingFailingStdin) written() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.frames, "")
}

// The publication of a request whose automatic approval failed can fail too.
// The request then waits for an answer that no reader can give, so the agent
// withdraws it and aborts the session's turn. A replacement that arrived
// meanwhile is another request with a card of its own: it stays pending, and
// the turn that waits for it goes on.
func TestNativeCopilotFailedFallbackPublicationLeavesItsReplacement(t *testing.T) {
	for name, replaced := range map[string]bool{"a replaced request": true, "the current request": false} {
		t.Run(name, func(t *testing.T) {
			sink := newCopilotInstanceSink()
			sink.failFirst = errors.New("storage unavailable")
			stdin := &recordingFailingStdin{}
			a := &Agent{
				copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
					Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: stdin}),
				}},
				sink: agentapi.NewProviderServices(sink), sessionID: "session",
				options: optionmap.Map{agentapi.OptionIDPermissionMode: contracts.CopilotPermissionModeAllowAll},
			}
			first := &copilotPendingControl{spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "permission", instanceID: "first", payload: []byte(`{"first":true}`), ready: make(chan struct{})}
			close(first.ready)
			second := &copilotPendingControl{spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "permission", instanceID: "second", payload: []byte(`{"second":true}`), ready: make(chan struct{}), published: true}
			a.controls = map[string]*copilotPendingControl{"control": first}
			done := make(chan struct{})
			go func() {
				a.approvePendingPermissions()
				close(done)
			}()
			awaitCopilotSink(t, sink.entered)
			if replaced {
				a.controlMu.Lock()
				a.controls["control"] = second
				a.controlMu.Unlock()
			}
			close(sink.release)
			select {
			case <-done:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the approval did not finish")
			}
			a.controlMu.Lock()
			current := a.controls["control"]
			a.controlMu.Unlock()
			require.Contains(t, stdin.written(), `"method":"session.permissions.handlePendingPermissionRequest"`, "the automatic approval was tried")
			if replaced {
				require.Same(t, second, current, "the replacement stays pending")
				require.Empty(t, sink.CanceledControls(), "the replacement keeps its card")
				require.NotContains(t, stdin.written(), `"method":"session.abort"`, "the turn that waits for the replacement goes on")
				return
			}
			require.Nil(t, current, "the request that no reader can see is withdrawn")
			require.Contains(t, stdin.written(), `"method":"session.abort"`, "the turn no longer waits for a request that no reader can see")
		})
	}
}

// A stopped agent refuses the stop, the way every other provider does. The
// worker maps that refusal to "not running"; a nil reported a stop that never
// happened, and the handler then withdrew a dead process's prompts on the
// strength of it.
func TestNativeCopilotInterruptRefusesAStoppedAgent(t *testing.T) {
	agent := newCopilotControlAgent(agentapi.NewProviderServices(&agenttest.ControlSink{}))
	agent.SetStoppedForTest(true)

	require.ErrorContains(t, agent.Interrupt(agentapi.StopContext{}), "stopped")
}

func TestNativeCopilotFailedControlPublicationKeepsNoPendingEntry(t *testing.T) {
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	agent := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question"}}}}`))
	require.Zero(t, sink.PublishedControlCount())
	require.Empty(t, agent.controls)
}

func TestNativeCopilotClosingProcessKeepsLateControlsOutOfTheUI(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	agent.closing = true
	raw := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question"}}}}`)
	agent.HandleOutput(raw)
	require.Zero(t, sink.PublishedControlCount())
	require.Len(t, sink.Messages(), 1)
	require.Equal(t, raw, sink.Messages()[0].Content)
}

func TestNativeCopilotPublishesNativeControlEvents(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	ids := make(map[string]struct{})
	kinds := []string{"permission", "user_input", "exit_plan_mode", "elicitation", "auto_mode_switch", "sampling"}
	for index, kind := range kinds {
		raw := []byte(fmt.Sprintf(` {"method":"session.event","params":{"sessionId":"session","event":{"type":"%s.requested","data":{"requestId":"shared-id","unknown":9007199254740993,"zero":0,"boolean":false,"empty":""}}}} `, kind))
		agent.HandleOutput(raw)
		require.Equal(t, index+1, sink.PublishedControlCount(), kind)
		request := sink.LastPublishedControl()
		require.NotEmpty(t, request.RequestID, kind)
		require.Equal(t, raw, request.Payload)
		ids[request.RequestID] = struct{}{}
	}
	require.Equal(t, len(kinds), sink.PublishedControlCount())
	require.Len(t, ids, len(kinds), "native control kinds must not share a response identity")
	agenttest.AssertPublishedControlKeys(t, copilotProvider{}, sink)
}

func TestNativeCopilotControlReannouncementRetainsItsPayload(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	first := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"id":"event-1","type":"user_input.requested","data":{"requestId":"question","question":"Choose a color."}}}}`)
	agent.HandleOutput(first)
	require.Equal(t, 1, sink.PublishedControlCount())
	identifier := sink.LastPublishedControl().RequestID
	agent.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"id":"event-2","type":"user_input.requested","data":{"requestId":"question","question":"Choose a color."}}}}`))
	require.Equal(t, 2, sink.PublishedControlCount())
	require.Equal(t, identifier, sink.LastPublishedControl().RequestID)
	require.Equal(t, first, sink.LastPublishedControl().Payload)
	revised := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"id":"event-3","type":"user_input.requested","data":{"requestId":"question","question":"Choose a different color."}}}}`)
	agent.HandleOutput(revised)
	require.Equal(t, identifier, sink.LastPublishedControl().RequestID)
	require.Equal(t, revised, sink.LastPublishedControl().Payload)
}

func TestNativeCopilotControlsRejectForeignAndResolvedRequests(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	for _, raw := range []string{
		`{"method":"session.event","params":{"sessionId":"foreign","event":{"type":"permission.requested","data":{"requestId":"request"}}}}`,
		`{"method":"session.event","params":{"sessionId":"session","event":{"type":"permission.requested","data":{"requestId":"request","resolvedByHook":true}}}}`,
		`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{}}}}`,
	} {
		agent.HandleOutput([]byte(raw))
	}
	require.Zero(t, sink.PublishedControlCount())
}

func TestAutoModeSwitchAskPublishesACard(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	// Trimmed from .tmp/verify-c29/copilot/run3/frames.jsonl.
	raw := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"auto_mode_switch.requested","data":{"requestId":"switch-1","errorCode":"user_model_rate_limited","retryAfterSeconds":1},"ephemeral":true}}}`)
	a.HandleOutput(raw)
	require.Equal(t, 1, sink.PublishedControlCount())
	require.Equal(t, raw, sink.LastPublishedControl().Payload)
}

func TestAutoModeSwitchCompletedWithdrawsTheCard(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"auto_mode_switch.requested","data":{"requestId":"switch-1"}}}}`))
	require.Equal(t, 1, sink.PublishedControlCount())
	id := sink.LastPublishedControl().RequestID
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"auto_mode_switch.completed","data":{"requestId":"switch-1"}}}}`))
	require.Equal(t, []string{id}, sink.CanceledControls())
	require.Empty(t, a.controls)
}

// samplingWaitStdin signals that the request reached the transport and sends no reply.
type samplingWaitStdin struct {
	agenttest.Stdin
	once    sync.Once
	written chan struct{}
}

func (s *samplingWaitStdin) Write(raw []byte) (int, error) {
	n, err := s.Stdin.Write(raw)
	s.once.Do(func() { close(s.written) })
	return n, err
}

func TestSamplingStopCancelsAnInferenceThatHasNoReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	stdin := &samplingWaitStdin{written: make(chan struct{})}
	sink := &agenttest.ControlSink{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(sink))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		Ctx: ctx, Cancel: cancel, Stdin: stdin, ProcessDone: done,
	})
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"sampling.requested","data":{"requestId":"sample-1","serverName":"sampler","mcpRequestId":"sampling-1","request":{"messages":[],"maxTokens":20}}}}}`))
	require.Equal(t, 1, sink.PublishedControlCount())
	id := sink.LastPublishedControl().RequestID
	answered := make(chan error, 1)
	go func() {
		answered <- a.SendRawInput(sink.Answer(id, fmt.Appendf(nil, `{"response":{"request_id":%q,"response":{"decision":"allow"}}}`, id)))
	}()
	deadline, endDeadline := context.WithTimeout(t.Context(), 30*time.Second)
	defer endDeadline()
	select {
	case <-stdin.written:
	case <-deadline.Done():
		t.Fatal("the sampling request did not reach the transport")
	}
	require.Contains(t, stdin.String(), `"method":"session.mcp.executeSampling"`)
	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	// synctest cannot detect this wait because an RWMutex wait is not durable.
	// The deadline detects a stuck stop; it does not control a product state window.
	select {
	case err := <-answered:
		require.Error(t, err)
	case <-deadline.Done():
		t.Error("Stop did not cancel the sampling request")
	}
	require.NoError(t, ctx.Err(), "sampling cancellation must keep graceful shutdown available")
	require.NotContains(t, stdin.String(), `"method":"session.ui.handlePendingSampling"`)
	// End the fake process so both calls return after an assertion fails.
	a.SetStoppedForTest(true)
	cancel()
	close(done)
	<-stopped
}

func TestSamplingContextCannotRestartAfterAConcurrentStop(t *testing.T) {
	t.Parallel()
	a := newCopilotControlAgent(agentapi.NewProviderServices(&agenttest.ControlSink{}))
	done := make(chan struct{})
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: done})
	a.sessionMu.Lock()
	stopped := make(chan struct{})
	go func() { a.Stop(); close(stopped) }()
	var callers sync.WaitGroup
	contexts := make(chan context.Context, 32)
	for range cap(contexts) {
		callers.Go(func() { contexts <- a.samplingContext() })
	}
	callers.Wait()
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-a.samplingContext().Done():
	case <-deadline.Done():
		t.Error("Stop did not cancel sampling before it took the session lock")
	}
	close(contexts)
	for ctx := range contexts {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("a concurrent caller received an uncancelled sampling context")
		}
	}
	a.SetStoppedForTest(true)
	close(done)
	a.sessionMu.Unlock()
	<-stopped
	require.ErrorIs(t, a.samplingContext().Err(), context.Canceled)
}

func TestSamplingAfterCancellationWritesNoRequest(t *testing.T) {
	t.Parallel()
	stdin := &agenttest.Stdin{}
	a := newCopilotControlAgent(agentapi.NewProviderServices(&agenttest.ControlSink{}))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), Stdin: stdin})
	a.samplingContext()
	a.samplingCancel()
	pending := &copilotPendingControl{sessionID: "session", nativeRequestID: "sample", data: []byte(`{"serverName":"sampler","mcpRequestId":1,"request":{"messages":[]}}`)}
	_, err := a.resolveSampling(pending, []byte(`{"decision":"allow"}`))
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, agentapi.ErrDeliveryUncertain)
	require.Empty(t, stdin.String())
}

func TestCopilotAnswerControlReturnsReplacedWhenSupersededDuringWait(t *testing.T) {
	t.Parallel()
	a := newCopilotControlAgent(agentapi.NewProviderServices(&agenttest.ControlSink{}))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context()})
	a.sessionID = "session"
	oldPending := &copilotPendingControl{
		spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "req-1",
		instanceID: "old-instance", ready: make(chan struct{}), published: true,
	}
	newPending := &copilotPendingControl{
		spec: copilotControlSpecs[0], sessionID: "session", nativeRequestID: "req-1",
		instanceID: "new-instance", ready: make(chan struct{}), published: true,
	}
	close(oldPending.ready)
	close(newPending.ready)
	a.controls = map[string]*copilotPendingControl{"ctrl-1": newPending}

	a.sessionMu.RLock()
	err := a.answerNativeControl("ctrl-1", oldPending, []byte(`{"kind":"approve-once"}`), false)
	a.sessionMu.RUnlock()
	require.ErrorIs(t, err, agentapi.ErrControlInstanceReplaced,
		"answering a control superseded during wait must return ErrControlInstanceReplaced")
}
