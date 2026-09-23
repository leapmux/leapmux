package copilot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
func newCopilotControlAgent(sink agent.ProviderServices) *Agent {
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
					result <- agent.SendRawInput([]byte(`{"response":{"request_id":"control","response":{"answer":"old answer","wasFreeform":true}}}`))
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
	agent := newCopilotControlAgent(agent.NewProviderServices(&agenttest.ControlSink{}))
	agent.SetStoppedForTest(true)

	require.ErrorContains(t, agent.Interrupt(), "stopped")
}

func TestNativeCopilotFailedControlPublicationKeepsNoPendingEntry(t *testing.T) {
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	agent := newCopilotControlAgent(agent.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question"}}}}`))
	require.Zero(t, sink.PublishedControlCount())
	require.Empty(t, agent.controls)
}

func TestNativeCopilotClosingProcessKeepsLateControlsOutOfTheUI(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agent.NewProviderServices(sink))
	agent.closing = true
	raw := []byte(`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{"requestId":"question"}}}}`)
	agent.HandleOutput(raw)
	require.Zero(t, sink.PublishedControlCount())
	require.Len(t, sink.Messages(), 1)
	require.Equal(t, raw, sink.Messages()[0].Content)
}

func TestNativeCopilotPublishesNativeControlEvents(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCopilotControlAgent(agent.NewProviderServices(sink))
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
	agent := newCopilotControlAgent(agent.NewProviderServices(sink))
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
	agent := newCopilotControlAgent(agent.NewProviderServices(sink))
	for _, raw := range []string{
		`{"method":"session.event","params":{"sessionId":"foreign","event":{"type":"permission.requested","data":{"requestId":"request"}}}}`,
		`{"method":"session.event","params":{"sessionId":"session","event":{"type":"permission.requested","data":{"requestId":"request","resolvedByHook":true}}}}`,
		`{"method":"session.event","params":{"sessionId":"session","event":{"type":"user_input.requested","data":{}}}}`,
	} {
		agent.HandleOutput([]byte(raw))
	}
	require.Zero(t, sink.PublishedControlCount())
}
