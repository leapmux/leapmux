package codebuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codebuddyControlPeer stands in for the stdin of a CodeBuddy process. It
// records each frame that the agent writes. It answers each control_request
// with the frame that answer returns for the request id, as the control
// dispatcher of CodeBuddy does. The answer arrives on a goroutine of its own,
// as the reader loop delivers it. A nil answer, or an empty frame, sends no
// answer.
type codebuddyControlPeer struct {
	agenttest.Stdin
	answer  func(requestID string) string
	agent   atomic.Pointer[Agent]
	replies sync.WaitGroup
}

func (p *codebuddyControlPeer) Write(data []byte) (int, error) {
	n, err := p.Stdin.Write(data)
	if p.answer == nil {
		return n, err
	}
	var frame struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &frame) != nil || frame.Type != frameTypeControlRequest {
		return n, err
	}
	reply := p.answer(frame.RequestID)
	target := p.agent.Load()
	if reply == "" || target == nil {
		return n, err
	}
	p.replies.Go(func() { target.HandleOutput([]byte(reply)) })
	return n, err
}

// codebuddyControlAnswer answers a control request with success and the
// response payload that response gives.
func codebuddyControlAnswer(response string) func(string) string {
	return func(requestID string) string {
		return `{"type":"control_response","response":{"subtype":"success","request_id":"` + requestID + `","response":` + response + `}}`
	}
}

// codebuddyControlFailure answers a control request with CodeBuddy's error
// form, which its dispatcher sends when a handler throws.
func codebuddyControlFailure(message string) func(string) string {
	return func(requestID string) string {
		return `{"type":"control_response","response":{"subtype":"error","request_id":"` + requestID + `","error":` + agenttest.JSONString(message) + `}}`
	}
}

// codebuddySteered is CodeBuddy's answer to a steer that it took into the
// running turn.
const codebuddySteered = `{"session_id":"session-1","steered":true}`

// newCodebuddySteerAgent returns an agent in a running turn whose stdin is a
// control peer. A nil clock selects the real clock.
func newCodebuddySteerAgent(t *testing.T, clock quartz.Clock, answer func(string) string) (*Agent, *codebuddyControlPeer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	processDone := make(chan struct{})
	peer := &codebuddyControlPeer{answer: answer}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "codebuddy", Ctx: ctx, Cancel: cancel,
			Stdin: peer, ProcessDone: processDone, Clock: clock,
		}),
		sink:           agent.NewProviderServices(&agenttest.Sink{}),
		sessionID:      "session-1",
		active:         true,
		pendingControl: make(map[string]chan<- codebuddyControlResult),
	}
	peer.agent.Store(a)
	t.Cleanup(func() {
		// The exit channel stays open while the test runs, because the wait for
		// an answer ends at a process exit. A fake process never calls Wait, so
		// Stop needs the closed channel.
		close(processDone)
		a.Process.Stop()
		peer.replies.Wait()
		cancel()
	})
	return a, peer
}

func TestCodebuddySteerInputReadsTheAnswerOfCodeBuddy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer func(string) string
		check  func(t *testing.T, err error)
	}{
		{
			name:   "a steer that CodeBuddy took into the turn succeeds",
			answer: codebuddyControlAnswer(codebuddySteered),
			check:  func(t *testing.T, err error) { assert.NoError(t, err) },
		},
		{
			name:   "a steer that CodeBuddy refused goes to the next turn",
			answer: codebuddyControlAnswer(`{"session_id":"session-1","steered":false}`),
			check: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, agent.ErrSteeringUnsupported,
					"CodeBuddy refuses a slash command or a steer with no text, and the queue then sends it as the next turn")
			},
		},
		{
			name:   "a steer that found no running turn is an ended turn",
			answer: codebuddyControlAnswer(`{"session_id":"session-1","steered":false,"reason":"idle"}`),
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, agent.ErrNoActiveTurn) },
		},
		{
			name:   "a steer for a turn that already ended is an ended turn",
			answer: codebuddyControlAnswer(`{"session_id":"session-1","steered":false,"reason":"stale"}`),
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, agent.ErrNoActiveTurn) },
		},
		{
			name:   "a refusal for a reason that this build does not know still goes to the next turn",
			answer: codebuddyControlAnswer(`{"session_id":"session-1","steered":false,"reason":"queue-full"}`),
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, agent.ErrSteeringUnsupported) },
		},
		{
			name:   "an answer that states no outcome leaves the delivery uncertain",
			answer: codebuddyControlAnswer(`{"session_id":"session-1"}`),
			check: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, agent.ErrDeliveryUncertain, "CodeBuddy may hold the steer, so a resend could duplicate it")
			},
		},
		{
			name:   "an error that CodeBuddy returned fails the steer with its message",
			answer: codebuddyControlFailure("Session mismatch: expected session-1, got session-2"),
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "Session mismatch: expected session-1, got session-2")
				assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "CodeBuddy refused the request before it buffered the steer")
				assert.NotErrorIs(t, err, agent.ErrSteeringUnsupported)
				assert.NotErrorIs(t, err, agent.ErrNoActiveTurn)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, peer := newCodebuddySteerAgent(t, nil, tc.answer)
			tc.check(t, a.SteerInput("also check the tests", nil))
			assert.Contains(t, peer.String(), `"subtype":"steer"`, "the steer reached CodeBuddy")
		})
	}
}

func TestCodebuddySteerInputWithoutAnAnswerIsUncertain(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	// The agent comes first, so that its cleanup runs after the traps close.
	// The traps catch every timer, and Process.Stop arms a timer of its own,
	// which an open trap would hold for ever.
	a, peer := newCodebuddySteerAgent(t, clock, nil)
	newTimer := clock.Trap().NewTimer()
	stopTimer := clock.Trap().TimerStop()
	t.Cleanup(func() {
		newTimer.Close()
		stopTimer.Close()
	})
	ctx := testutil.DeadlineContext(t)

	result := make(chan error, 1)
	go func() { result <- a.SteerInput("also check the tests", nil) }()

	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, a.APITimeout(), delay, "the steer waits for its answer as long as any other request")
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, agent.ErrDeliveryUncertain, "CodeBuddy may hold the steer, so a resend could duplicate it")
	case <-ctx.Done():
		t.Fatal("SteerInput did not return after the wait for its answer expired")
	}
	assert.Contains(t, peer.String(), `"subtype":"steer"`, "the steer reached CodeBuddy before the wait")
}

// TestCodebuddySteerInputWithAFailedWriteIsUncertain pins the outcome of a
// steer whose write fails while the process still runs. CodeBuddy sent no
// answer, so the delivery is uncertain, as it is for a failed write of
// SendInput.
func TestCodebuddySteerInputWithAFailedWriteIsUncertain(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "codebuddy", Ctx: ctx, Cancel: cancel,
			Stdin: agenttest.FailingStdin{}, ProcessDone: processDone, Clock: clock,
		}),
		sink:           agent.NewProviderServices(&agenttest.Sink{}),
		sessionID:      "session-1",
		active:         true,
		pendingControl: make(map[string]chan<- codebuddyControlResult),
	}
	t.Cleanup(func() {
		close(processDone)
		a.Process.Stop()
		cancel()
	})
	// The traps come after the agent, so that they close before Process.Stop
	// arms its own timer in the cleanup.
	newTimer := clock.Trap().NewTimer()
	stopTimer := clock.Trap().TimerStop()
	t.Cleanup(func() {
		newTimer.Close()
		stopTimer.Close()
	})
	deadline := testutil.DeadlineContext(t)

	result := make(chan error, 1)
	go func() { result <- a.SteerInput("also check the tests", nil) }()

	delay := testutil.WaitForTimer(t, deadline, newTimer)
	assert.Equal(t, codebuddyControlExitWait, delay, "a failed write waits a moment for the exit that can explain it")
	testutil.AdvanceAndAwaitStop(t, deadline, clock, delay, stopTimer)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-deadline.Done():
		t.Fatal("SteerInput did not return after the failed write")
	}
}

// TestCodebuddySteerInputRefusesAFileThatSteeringDrops pins the refusal of
// each file that the steer drain of CodeBuddy drops. The drain keeps the text
// of the blocks only (ny and ly in 2.160.0), so an image, a PDF or a binary
// file would lose its bytes while its label still tells the model that a file
// is attached. The queue then sends the input as the next turn, files and all.
func TestCodebuddySteerInputRefusesAFileThatSteeringDrops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		attachments []*leapmuxv1.Attachment
	}{
		{name: "an image", attachments: []*leapmuxv1.Attachment{codebuddyOrderPNG()}},
		{name: "a PDF", attachments: []*leapmuxv1.Attachment{codebuddyOrderPDF()}},
		{name: "a binary file", attachments: []*leapmuxv1.Attachment{codebuddyOrderBlob()}},
		{name: "an image after a text file", attachments: []*leapmuxv1.Attachment{codebuddyOrderNotes(), codebuddyOrderPNG()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
			assert.ErrorIs(t, a.SteerInput(codebuddyOrderPrompt, tc.attachments), agent.ErrSteeringUnsupported)
			assert.Empty(t, peer.String(), "a refused steer writes nothing")
		})
	}
}

// TestCodebuddySteerInputChecksTheFilesBeforeTheTurn pins the order of the two
// refusals. SteerInput refuses a steer that the drain cannot carry as
// unsupported also when no turn runs, so that answer depends on the input
// alone.
func TestCodebuddySteerInputChecksTheFilesBeforeTheTurn(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
	a.setTurnActive(false)
	assert.ErrorIs(t, a.SteerInput(codebuddyOrderPrompt, []*leapmuxv1.Attachment{codebuddyOrderPNG()}), agent.ErrSteeringUnsupported)
	assert.ErrorIs(t, a.SteerInput(codebuddyOrderPrompt, []*leapmuxv1.Attachment{codebuddyOrderNotes()}), agent.ErrNoActiveTurn)
	assert.Empty(t, peer.String())
}
