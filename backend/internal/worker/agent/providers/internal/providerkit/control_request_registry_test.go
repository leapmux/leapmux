package providerkit

import (
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRegistryBase gives a JSONRPCProcess whose stdin a test can read back.
func newRegistryBase() (*JSONRPCProcess, *agenttest.Stdin) {
	stdin := &agenttest.Stdin{}
	return &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{AgentID: "agent", Stdin: stdin})}, stdin
}

// drainStdin returns once every frame queued before it reached the fake stdin.
//
// One goroutine performs the writes in order, so a SYNCHRONOUS write that lands
// proves that each detached frame ahead of it landed too. The frame carries no
// JSON-RPC id, so it answers nothing and leaves the registry as it was.
func drainStdin(t *testing.T, base *JSONRPCProcess) {
	t.Helper()
	require.NoError(t, base.WriteStdin([]byte("{}\n")))
}

// A request the provider does not block on takes no answer, and its card still goes.
func TestControlRegistryWithoutACancelAnswerSendsNothing(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":"q-1","method":"cursor/ask_question"}`), nil)
	base.WithdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String())
	assert.Equal(t, []string{`jsonrpc:"q-1"`}, sink.CanceledControls())
}

// stealingPublishSink drains the registry from INSIDE PublishControlRequest, which
// is the window a stop lands in: the record is registered, and the card does not
// exist yet.
type stealingPublishSink struct {
	agenttest.ControlSink
	base *JSONRPCProcess
}

func (s *stealingPublishSink) PublishControlRequest(request agent.ControlRequest) error {
	s.base.WithdrawAllControlRequests(&s.ControlSink)
	s.ResetCanceledControls() // The stop's own cancel found no row; only the publish's counts.
	return nil
}

// answeringPublishSink ANSWERS the request from inside PublishControlRequest, which is
// the window a fast reader lands in: the card is broadcast, and the publisher has not
// yet re-taken outstandingMu.
type answeringPublishSink struct {
	agenttest.ControlSink
	base *JSONRPCProcess
}

func (s *answeringPublishSink) PublishControlRequest(agent.ControlRequest) error {
	// The ordinary answer path: the frame carries the request's own id, and
	// SendRawInput forgets the record after it writes.
	if err := s.base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":"allow"}}`)); err != nil {
		return err
	}
	return nil
}

// The agent withdrew the request itself, so it waits for no answer.
func TestControlRegistryWithdrawalWithoutAnAnswerOnlyDropsTheCard(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())
	base.WithdrawControlRequest(sink, "jsonrpc:7")
	assert.Empty(t, stdin.String())
	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls())
	// The record is gone, so a later stop cannot answer it a second time.
	base.WithdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String())
}

// A publication that storage refused leaves no record behind.
func TestControlRegistryDropsTheRecordWhenPublicationFails(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())
	// The refusal answers the provider DETACHED, so that frame lands on the writer's
	// own goroutine. Waiting for it is what makes the reset below take it: without
	// the wait the frame could arrive after the reset and fail the assertion, or
	// before it and pass one that proved nothing.
	drainStdin(t, base)
	stdin.Reset()
	base.WithdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String(), "a request that was never published must not receive a cancel answer")
	assert.Empty(t, sink.CanceledControls())
}

// The answered request leaves the registry only once the write lands, so a failed
// write keeps it available for another answer.
func TestControlRegistryForgetsAnAnsweredRequestAfterTheWrite(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())
	require.NoError(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"once"}}}`)))
	stdin.Reset()
	base.WithdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String(), "the answered request must not receive a second answer")
}

func TestControlRegistryKeepsARequestWhoseAnswerCouldNotBeWritten(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{AgentID: "agent", Stdin: agenttest.FailingStdin{}})}
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())
	require.Error(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`)))
	base.outstandingMu.Lock()
	_, found := base.outstandingControls["jsonrpc:7"]
	base.outstandingMu.Unlock()
	assert.True(t, found, "a write that failed leaves the request waiting, so the reader can answer it again")
}

// A withdrawal that lands between the registration and the publish cancelled a
// card that did not exist yet: CancelControlRequest found no row and returned in
// silence, and the publish then created the card it meant to retire. The result
// was a live card with no record behind it, which no later withdrawal could reach
// and the reader could never dismiss.
func TestControlRegistryRetiresACardAStopStoleDuringThePublish(t *testing.T) {
	t.Parallel()

	base, _ := newRegistryBase()
	sink := &stealingPublishSink{base: base}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())

	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls(),
		"the publish must retire a card whose record a stop already took")
}

// The OTHER half of the same window: the reader answers while the publish is still
// in flight.
//
// PublishControlRequest completes the browser broadcast before it returns, so the card
// is on screen and answerable inside the window. Every control response reaches the
// provider through SendRawInput, which removes the record -- so a bare "is the record
// still there?" test read an ANSWERED request as a stolen one and cancelled it. The
// provider had the answer, but the transcript row was deleted and a controlCancel was
// broadcast, so the card the reader had just allowed vanished as CANCELLED.
func TestControlRegistryKeepsACardTheReaderAnsweredDuringThePublish(t *testing.T) {
	t.Parallel()

	base, _ := newRegistryBase()
	sink := &answeringPublishSink{base: base}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), MCPElicitationCancelAnswer())

	assert.Empty(t, sink.CanceledControls(), "an answer inside the publish window must not cancel the card")
	base.outstandingMu.Lock()
	defer base.outstandingMu.Unlock()
	assert.NotContains(t, base.outstandingControls, "jsonrpc:7", "the answer still retires the record")
}

// The registry writes the cancel answer that each request carries, whatever its
// kind. Each provider pins its own answer end to end:
// TestACPInterruptAnswersEveryOpenControlRequestByKind and
// TestCursorPlanRequestCarriesARejectedCancelAnswer.
func TestControlRegistryAnswersEachKindWithItsOwnCancelAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer any
		want   string
	}{
		{"mcp elicitation", MCPElicitationCancelAnswer(), `{"action":"cancel"}`},
		{"another kind", map[string]string{"decision": "denied"}, `{"decision":"denied"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base, stdin := newRegistryBase()
			sink := &agenttest.ControlSink{}
			base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"request"}`), tc.answer)
			require.Len(t, sink.PublishedControls(), 1)
			base.WithdrawAllControlRequests(sink)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":`+tc.want+`}`, stdin.String())
			assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls())
		})
	}
}

// An open request that the provider withdraws by its own event loses its card, and
// the provider gets no answer: it withdrew the request itself.
func TestWithdrawOutstandingControlRequestRetiresAnOpenRequest(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), map[string]any{"outcome": "cancelled"})

	assert.True(t, base.WithdrawOutstandingControlRequest(sink, "jsonrpc:7"))
	drainStdin(t, base)
	assert.Equal(t, "{}\n", stdin.String(), "the provider withdrew the request, so nothing answers it")
	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls())

	// A second withdrawal finds nothing open and cancels nothing again.
	assert.False(t, base.WithdrawOutstandingControlRequest(sink, "jsonrpc:7"))
	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls())
}

// The reader answered first. The provider's event then echoes that answer, and the
// card the reader decided must not read as cancelled.
func TestWithdrawOutstandingControlRequestLeavesAnAnsweredRequest(t *testing.T) {
	t.Parallel()
	base, _ := newRegistryBase()
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), nil)
	require.NoError(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`)))

	assert.False(t, base.WithdrawOutstandingControlRequest(sink, "jsonrpc:7"))
	assert.Empty(t, sink.CanceledControls())
}

// An id the registry never held is not an open request.
func TestWithdrawOutstandingControlRequestIgnoresAnUnknownRequest(t *testing.T) {
	t.Parallel()
	base, _ := newRegistryBase()
	sink := &agenttest.ControlSink{}

	assert.False(t, base.WithdrawOutstandingControlRequest(sink, "jsonrpc:404"))
	assert.Empty(t, sink.CanceledControls())
}
