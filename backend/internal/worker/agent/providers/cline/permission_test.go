package cline

import (
	"errors"
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
			answer := r.sink.Answer("request", payload)
			require.Error(t, r.agent.SendRawInput(answer))
			assert.NoError(t, r.agent.SendRawInput(answer))
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
		done <- r.agent.SendRawInput(r.sink.Answer("request", mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "request", Approved: true})))
	}()
	<-entered
	r.agent.cancel()
	assert.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
	assert.Len(t, r.hub.commandsNamed(commandApprovalRespond), 1)
}

func TestClineRefusesUnstatedOrReplacedControlInstance(t *testing.T) {
	r := newRig(t)
	r.feed(t, contracts.ClineEventApprovalRequested, approval("request", "editor"))
	published := r.sink.LastPublishedControl()
	payload := mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "request", Approved: true})

	require.ErrorIs(t, r.agent.SendRawInput(agent.RawInput{Data: payload}), agent.ErrControlInstanceUnstated)
	require.ErrorIs(t, r.agent.SendRawInput(agent.RawInput{Data: payload, ControlInstanceID: "stale-instance"}), agent.ErrControlInstanceReplaced)
	assert.Empty(t, r.hub.commandsNamed(commandApprovalRespond))

	require.NoError(t, r.agent.SendRawInput(agent.RawInput{Data: payload, ControlInstanceID: published.InstanceID}))
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
func (s *publicationLifecycleSink) CancelControlRequestInstance(id, instanceID string) {
	s.ControlSink.CancelControlRequestInstance(id, instanceID)
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
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
	err := r.agent.SendRawInput(r.sink.Answer("request", mustJSON(t, contracts.ClineCapabilityReply{RequestId: "request", Ok: true})))
	assert.ErrorIs(t, err, errUnknownControl)
	assert.Equal(t, []string{"request"}, r.sink.CanceledControls())
}

func TestClineUnknownControlWrapsErrControlRequestNotPending(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	err := r.agent.SendRawInput(agent.RawInput{Data: mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "missing", Approved: true})})
	assert.ErrorIs(t, err, agent.ErrControlRequestNotPending)
}

func TestClineManualFallbackWaitsForPublication(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var published atomic.Bool
	r := newRig(t, func(config *rigConfig) {
		config.services = func(base *agenttest.ControlSink) agent.ServiceFacets {
			return &publicationLifecycleSink{ControlSink: base, before: func() {
				close(entered)
				<-release
				published.Store(true)
			}}
		}
	})
	r.agent.Mu.Lock()
	r.agent.settings.permissionMode = contracts.ClinePermissionModeAutoApprove
	r.agent.Mu.Unlock()
	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
		return fakeReply{Code: "busy", Message: "try again"}
	})
	r.feed(t, contracts.ClineEventApprovalRequested, approval("req-fallback", "editor"))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("manual fallback did not enter PublishControlRequest")
	}
	r.agent.Mu.Lock()
	control := r.agent.controls["req-fallback"]
	instanceID := control.instanceID
	pubDone := control.publicationDone
	r.agent.Mu.Unlock()

	select {
	case <-pubDone:
		t.Fatal("publicationDone closed before PublishControlRequest returned")
	default:
	}
	assert.Zero(t, r.sink.PublishedControlCount(), "control must not be published before release")

	var sawPublished atomic.Bool
	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
		sawPublished.Store(published.Load())
		return fakeReply{}
	})
	started := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		close(started)
		errCh <- r.agent.SendRawInput(agent.RawInput{
			Data:              mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "req-fallback", Approved: true}),
			ControlInstanceID: instanceID,
		})
	}()
	<-started
	select {
	case err := <-errCh:
		close(release)
		t.Fatalf("SendRawInput returned before fallback publication completed: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-errCh)
	assert.True(t, sawPublished.Load(), "manual approval must wait until fallback publication finishes")
	assert.Equal(t, 1, r.sink.PublishedControlCount())
	replies := approvalReplies(t, r.hub)
	require.Len(t, replies, 2, "expected failed automatic attempt followed by manual approval")
	assert.Equal(t, contracts.ClineApprovalReply{ApprovalId: "req-fallback", Approved: true}, replies[1])
}

func TestClineConcurrentSendRawInputWakesUpOnAutoApprovalOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		reply func(r *rig) fakeReply
	}{
		{
			name:  "auto-approval succeeds",
			reply: func(*rig) fakeReply { return fakeReply{} },
		},
		{
			name: "auto-approval delivery uncertain",
			reply: func(r *rig) fakeReply {
				go r.hub.drop()
				return fakeReply{Hold: true}
			},
		},
		{
			name:  "auto-approval returns approval_not_found",
			reply: func(*rig) fakeReply { return fakeReply{Code: "approval_not_found", Message: "gone"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) {
				c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
			})
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
				entered <- struct{}{}
				<-release
				return tc.reply(r)
			})
			r.feed(t, contracts.ClineEventApprovalRequested, approval("req-auto", "editor"))
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("auto-approval did not reach hub")
			}
			r.agent.Mu.Lock()
			control := r.agent.controls["req-auto"]
			require.NotNil(t, control)
			instanceID := control.instanceID
			r.agent.Mu.Unlock()

			errCh := make(chan error, 1)
			go func() {
				errCh <- r.agent.SendRawInput(agent.RawInput{
					Data:              mustJSON(t, contracts.ClineApprovalReply{ApprovalId: "req-auto", Approved: true}),
					ControlInstanceID: instanceID,
				})
			}()
			close(release)
			err := <-errCh
			assert.ErrorIs(t, err, agent.ErrControlRequestNotPending)
			r.agent.background.Wait()
		})
	}
}

func TestClineAutoApprovalUncertainCancelsAlreadyPublishedControl(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Run tool.")
	r.feed(t, contracts.ClineEventApprovalRequested, approval("pub-uncertain", "editor"))
	require.Equal(t, 1, r.sink.PublishedControlCount())

	r.hub.handle(commandApprovalRespond, func(fakeCommand) fakeReply {
		go r.hub.drop()
		return fakeReply{Hold: true}
	})
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove))
	r.agent.background.Wait()

	assert.Equal(t, []string{"pub-uncertain"}, r.sink.CanceledControls())
}

func TestClineApprovePendingPermissionsConcurrentErrorsAndSkipsStale(t *testing.T) {
	t.Parallel()
	t.Run("concurrent dispatch joins all failures", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		sessionID := r.sessionID()
		r.agent.Mu.Lock()
		r.agent.settings.permissionMode = contracts.ClinePermissionModeAutoApprove
		r.agent.controls = map[string]*pendingControl{
			"a1": {kind: controlApproval, sessionID: sessionID, toolName: "editor", ready: true, published: true, instanceID: "i1", publicationDone: make(chan struct{})},
			"a2": {kind: controlApproval, sessionID: sessionID, toolName: "editor", ready: true, published: true, instanceID: "i2", publicationDone: make(chan struct{})},
			"a3": {kind: controlApproval, sessionID: sessionID, toolName: "editor", ready: true, published: true, instanceID: "i3", publicationDone: make(chan struct{})},
		}
		r.agent.Mu.Unlock()

		r.hub.handle(commandApprovalRespond, func(cmd fakeCommand) fakeReply {
			switch cmd.str(contracts.ClineApprovalReplyApprovalId) {
			case "a1":
				return fakeReply{Code: "err_one", Message: "first failed"}
			case "a2":
				return fakeReply{Code: "err_two", Message: "second failed"}
			default:
				return fakeReply{}
			}
		})

		err := r.agent.approvePendingPermissions()
		require.Error(t, err)
		assert.ErrorContains(t, err, "first failed")
		assert.ErrorContains(t, err, "second failed")
		var joined interface{ Unwrap() []error }
		require.True(t, errors.As(err, &joined))
		assert.Len(t, joined.Unwrap(), 2)
		assert.Equal(t, []string{"a3"}, r.sink.CanceledControls())
	})

	t.Run("skips withdrawn or stale session control", func(t *testing.T) {
		t.Parallel()
		var current *Agent
		r := newRig(t, func(config *rigConfig) {
			config.services = func(base *agenttest.ControlSink) agent.ServiceFacets {
				return &publicationLifecycleSink{ControlSink: base, before: func() {
					// Withdraw "withdrawn" after it was claimed, before its goroutine dispatches respondApproval.
					current.dropControl("withdrawn")
				}}
			}
		})
		current = r.agent
		sessionID := r.sessionID()
		r.agent.Mu.Lock()
		r.agent.settings.permissionMode = contracts.ClinePermissionModeAutoApprove
		r.agent.controls = map[string]*pendingControl{
			"stale-session": {kind: controlApproval, sessionID: "old-session", toolName: "editor", ready: true, published: true, instanceID: "i-stale", publicationDone: make(chan struct{})},
			"withdrawn":     {kind: controlApproval, sessionID: sessionID, toolName: "editor", ready: true, published: true, instanceID: "i-withdrawn", publicationDone: make(chan struct{})},
			"live":          {kind: controlApproval, sessionID: sessionID, toolName: "editor", ready: true, published: true, instanceID: "i-live", publicationDone: make(chan struct{})},
			"plan":          {kind: controlApproval, sessionID: sessionID, toolName: contracts.ClineToolSwitchToActMode, ready: true, published: false, instanceID: "i-plan", publicationDone: make(chan struct{})},
		}
		r.agent.Mu.Unlock()

		err := r.agent.approvePendingPermissions()
		require.NoError(t, err)
		replies := approvalReplies(t, r.hub)
		require.Len(t, replies, 1)
		assert.Equal(t, "live", replies[0].ApprovalId)
	})
}
