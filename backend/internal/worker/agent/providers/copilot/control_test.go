package copilot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
					spec: copilotControlSpecs[1], sessionID: "session", nativeRequestID: "question", ready: make(chan struct{}),
				}
				agent := &Agent{
					copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.FailingStdin{}})}},
					sessionID:         "session", controls: map[string]*copilotPendingControl{"control": pending},
				}
				result := make(chan error, 1)
				go func() {
					result <- agent.SendRawInput([]byte(`{"response":{"request_id":"control","response":{"answer":"old answer","wasFreeform":true}}}`), agentapi.StopContext{})
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
	for index, kind := range []string{"permission", "user_input", "exit_plan_mode", "elicitation"} {
		raw := []byte(fmt.Sprintf(` {"method":"session.event","params":{"sessionId":"session","event":{"type":"%s.requested","data":{"requestId":"shared-id","unknown":9007199254740993,"zero":0,"boolean":false,"empty":""}}}} `, kind))
		agent.HandleOutput(raw)
		require.Equal(t, index+1, sink.PublishedControlCount(), kind)
		request := sink.LastPublishedControl()
		require.NotEmpty(t, request.RequestID, kind)
		require.Equal(t, raw, request.Payload)
		ids[request.RequestID] = struct{}{}
	}
	require.Equal(t, 4, sink.PublishedControlCount())
	require.Len(t, ids, 4, "native control kinds must not share a response identity")
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
		answered <- a.SendRawInput(fmt.Appendf(nil, `{"response":{"request_id":%q,"response":{"decision":"allow"}}}`, id), agentapi.StopContext{})
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
