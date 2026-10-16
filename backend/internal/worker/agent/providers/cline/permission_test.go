package cline

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClineDefiniteReplyFailureKeepsTheManualRequest(t *testing.T) {
	for _, questionCase := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "question"}[questionCase], func(t *testing.T) {
			r := newRig(t)
			command := commandApprovalRespond
			payload := mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "request", Approved: true})
			if questionCase {
				command = commandCapabilityRespond
				payload = mustJSON(t, contracts.ClineCapabilityReply{RequestId: "request", Ok: true})
				r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "request"))
			} else {
				r.feed(t, contracts.ClineEventApprovalRequested, approval("request", "editor"))
			}
			var calls atomic.Int32
			r.hub.handle(command, func(fakeCommand) fakeReply {
				if calls.Add(1) == 1 {
					return fakeReply{Code: "temporarily_unavailable", Message: "The request still waits."}
				}
				return fakeReply{}
			})
			require.Error(t, r.agent.SendRawInput(payload, agent.StopContext{}))
			assert.NoError(t, r.agent.SendRawInput(payload, agent.StopContext{}))
			assert.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestClineFailedAutomaticReplyDoesNotRepeatForAnotherRequest(t *testing.T) {
	r := newRig(t)
	r.startTurn(t, "Run the tools.")
	r.feed(t, contracts.ClineEventApprovalRequested, approval("first", "editor"))
	var first atomic.Int32
	r.hub.handle(commandApprovalRespond, func(command fakeCommand) fakeReply {
		if command.str(contracts.ClineApprovalReplyApprovalId) == "first" {
			first.Add(1)
			return fakeReply{Code: "temporarily_unavailable", Message: "The request still waits."}
		}
		return fakeReply{}
	})
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove))
	r.agent.background.Wait()
	r.feed(t, contracts.ClineEventApprovalRequested, approval("plan", contracts.ClineToolSwitchToActMode))
	r.agent.background.Wait()
	assert.Equal(t, int32(1), first.Load(), "another publication cannot retry the refused automatic reply")
}

func TestClineLostReplyReportsUncertainDelivery(t *testing.T) {
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("request", "editor"))
	entered := make(chan struct{})
	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply { close(entered); return fakeReply{Hold: true} })
	done := make(chan error, 1)
	go func() {
		done <- r.agent.SendRawInput(mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "request", Approved: true}), agent.StopContext{})
	}()
	<-entered
	r.agent.cancel()
	assert.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
	assert.Len(t, r.hub.commandsNamed(commandApprovalRespond), 1)
}

type publicationLifecycleSink struct {
	*agenttest.ControlSink
	before func()
	mu     sync.Mutex
	active bool
}

func (s *publicationLifecycleSink) PublishControlRequest(request agent.ControlRequest) error {
	s.before()
	err := s.ControlSink.PublishControlRequest(request)
	s.mu.Lock()
	s.active = err == nil
	s.mu.Unlock()
	return err
}
func (s *publicationLifecycleSink) CancelControlRequest(id string) {
	s.ControlSink.CancelControlRequest(id)
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}
func (s *publicationLifecycleSink) CancelControlRequestInstance(id, _ string) {
	s.CancelControlRequest(id)
}

func TestClineRetirementDuringPublicationLeavesNoCard(t *testing.T) {
	for _, questionCase := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "question"}[questionCase], func(t *testing.T) {
			var current *Agent
			var sink *publicationLifecycleSink
			r := newRig(t, func(config *rigConfig) {
				config.services = func(base *agenttest.ControlSink) agent.ServiceFacets {
					sink = &publicationLifecycleSink{ControlSink: base, before: func() { current.withdrawAllControls() }}
					return sink
				}
			})
			current = r.agent
			if questionCase {
				r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "request"))
			} else {
				r.feed(t, contracts.ClineEventApprovalRequested, approval("request", "editor"))
			}
			sink.mu.Lock()
			active := sink.active
			sink.mu.Unlock()
			assert.False(t, active, "retirement must also cancel a publication that finishes afterwards")
		})
	}
}

func TestClineIgnoredQuestionAnswerIsNotReportedAsDelivered(t *testing.T) {
	r := newRig(t)
	r.feed(t, contracts.ClineEventCapabilityRequested, question(r, "request"))
	r.hub.handle(commandCapabilityRespond, func(fakeCommand) fakeReply {
		return fakeReply{Payload: map[string]any{"requestId": "request", "ignored": true}}
	})
	err := r.agent.SendRawInput(mustJSON(t, contracts.ClineCapabilityReply{RequestId: "request", Ok: true}), agent.StopContext{})
	assert.ErrorIs(t, err, errUnknownControl)
	assert.Equal(t, []string{"request"}, r.sink.CanceledControls())
}
