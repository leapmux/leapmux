package providerkit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlPublicationFailurePreservesWireID(t *testing.T) {
	t.Parallel()
	for _, wireID := range []string{`7`, `"007"`, `9007199254740993`} {
		t.Run(wireID, func(t *testing.T) {
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
			base := JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
			base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":`+wireID+`,"method":"request"}`), nil)
			// PublishControlRequest runs on the goroutine that drains the provider's
			// stdout, so it QUEUES this reply rather than waiting for the write.
			var answer string
			require.Eventually(t, func() bool {
				answer = output.String()
				return answer != ""
			}, 2*time.Second, 5*time.Millisecond, "the publication failure is answered")
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":`+wireID+`,"error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, answer)
			assert.Empty(t, sink.PublishedControls())
		})
	}
}

func TestJSONRPCControlPublicationDoesNotReplyOnSuccess(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &agenttest.ControlSink{}
	base := JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(output)}}
	payload := []byte(` {"jsonrpc":"2.0", "id":"007","method":"request"} `)
	base.PublishControlRequest(sink, payload, nil)
	// Never, not a bare Empty: a reply travels on the stdin writer, so an assertion
	// straight after this call cannot tell "no reply" from "not written yet".
	require.Never(t, func() bool { return output.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a published request draws no reply")
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, payload, sink.LastPublishedControl().Payload)
}

// The sink stores a request under the session that the caller states, and a
// request whose caller states none under the session that the sink holds. An
// agent can raise a request before the sink learns the session, so a caller
// that knows the session must be able to state it.
func TestJSONRPCControlPublicationStoresTheSessionOfTheRequest(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	sink.UpdateSessionID("held-session")
	base := JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(&agenttest.Stdin{})}}

	base.PublishControlRequestInSession(sink, "stated-session", []byte(`{"jsonrpc":"2.0","id":1,"method":"request"}`), nil)
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":2,"method":"request"}`), nil)
	base.PublishControlRequestInSession(sink, "", []byte(`{"jsonrpc":"2.0","id":3,"method":"request"}`), nil)

	sessions := map[string]string{}
	for _, record := range sink.PublishedControls() {
		sessions[record.RequestID] = record.AgentSessionID
	}
	assert.Equal(t, map[string]string{
		"jsonrpc:1": "stated-session",
		"jsonrpc:2": "held-session",
		"jsonrpc:3": "held-session",
	}, sessions)
	assert.Equal(t, 3, base.OutstandingControlCountForTest(), "each publication registers its request, whatever session it states")
}

func TestJSONRPCControlPublicationFailureWithoutWireIDDoesNotReply(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	base := JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(output)}}
	base.PublishControlRequest(sink, []byte(`{"method":"notification"}`), nil)
	require.Never(t, func() bool { return output.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a frame with no wire id draws no reply")
}

func TestJSONRPCControlPublicationHandlesReplyFailure(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	base := JSONRPCProcess{Process: Process{stdin: agenttest.FailingStdin{}}}
	require.NotPanics(t, func() {
		base.PublishControlRequest(sink, []byte(`{"id":7,"method":"request"}`), nil)
	})
	assert.Empty(t, sink.PublishedControls())
}

// gatedStdin holds each write until the test releases it, and then fails it with
// err after `written` bytes, or completes it when err is nil.
type gatedStdin struct {
	entered chan struct{}
	release chan struct{}
	written int
	err     error
}

func newGatedStdin(written int, err error) *gatedStdin {
	return &gatedStdin{entered: make(chan struct{}, 1), release: make(chan struct{}), written: written, err: err}
}

func (s *gatedStdin) Write(p []byte) (int, error) {
	s.entered <- struct{}{}
	<-s.release
	if s.err != nil {
		return s.written, s.err
	}
	return len(p), nil
}

func (*gatedStdin) Close() error { return nil }

// publishOne publishes one control request with a cancel answer and returns its key.
func publishOne(t *testing.T, base *JSONRPCProcess, sink *agenttest.ControlSink) string {
	t.Helper()
	base.PublishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"request"}`), map[string]any{"outcome": "cancelled"})
	require.Equal(t, 1, sink.PublishedControlCount())
	return sink.LastPublishedControl().RequestID
}

// An answer on its way to the provider already decides the request. A withdrawal
// that the provider reports during the write -- Grok Build reports that an
// interaction resolved, whoever resolved it -- must not cancel the card that the
// reader just answered.
func TestSendRawInputRetiresTheRecordBeforeTheWrite(t *testing.T) {
	t.Parallel()
	stdin := newGatedStdin(0, nil)
	sink := &agenttest.ControlSink{}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: stdin}}
	key := publishOne(t, base, sink)

	done := make(chan error, 1)
	go func() { done <- base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`), agent.StopContext{}) }()
	<-stdin.entered
	assert.False(t, base.WithdrawOutstandingControlRequest(sink, key), "the answer in flight decided the request")
	close(stdin.release)

	require.NoError(t, <-done)
	assert.Empty(t, sink.CanceledControls(), "the reader's card is answered, not cancelled")
	assert.False(t, base.OutstandingControlForTest(key))
}

// A write that certainly failed delivered nothing, so the request is still open,
// and a later withdrawal must still reach it. A write that may have delivered
// part of the answer leaves it retired, because the provider may have read it.
func TestSendRawInputRestoresTheRecordOfAFailedWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		written int
		open    bool
	}{
		{"nothing was written", 0, true},
		{"part of the answer was written", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := newGatedStdin(tc.written, errors.New("the pipe closed"))
			close(stdin.release)
			sink := &agenttest.ControlSink{}
			base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: stdin}}
			key := publishOne(t, base, sink)

			require.Error(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`), agent.StopContext{}))
			assert.Equal(t, tc.open, base.OutstandingControlForTest(key))
		})
	}
}

// A withdrawal of every request -- Stop -- that runs during a write that then
// fails must not see the request brought back afterwards.
func TestSendRawInputDoesNotRestoreARecordThatAWithdrawalOutran(t *testing.T) {
	t.Parallel()
	stdin := newGatedStdin(0, errors.New("the pipe closed"))
	sink := &agenttest.ControlSink{}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: stdin}}
	key := publishOne(t, base, sink)

	done := make(chan error, 1)
	go func() { done <- base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`), agent.StopContext{}) }()
	<-stdin.entered
	// On its own goroutine: a withdrawal that still found the record would queue
	// its cancel answer behind the held write, and wait for it.
	withdrawn := make(chan struct{})
	go func() {
		base.WithdrawAllControlRequests(sink)
		close(withdrawn)
	}()
	select {
	case <-withdrawn:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the withdrawal waited for the write of the answer")
	}
	close(stdin.release)

	require.Error(t, <-done)
	assert.False(t, base.OutstandingControlForTest(key))
}

// A frame that answers no open request has no record to put back, so a failed
// write of it adds nothing to the registry and leaves every other record alone.
func TestSendRawInputFailureOfAnUnknownAnswerAddsNoRecord(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: failingStdin{errors.New("the pipe closed")}}}
	key := publishOne(t, base, sink)

	require.Error(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":8,"result":{}}`), agent.StopContext{}))
	assert.True(t, base.OutstandingControlForTest(key), "the open request keeps its record")
	assert.Equal(t, 1, base.OutstandingControlCountForTest(), "the failed answer to an unknown id adds no record")

	require.Error(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","method":"notify"}`), agent.StopContext{}))
	assert.Equal(t, 1, base.OutstandingControlCountForTest(), "a frame with no id answers nothing")
}

// failingStdin fails every write at once and writes nothing.
type failingStdin struct{ err error }

func (s failingStdin) Write([]byte) (int, error) { return 0, s.err }

func (failingStdin) Close() error { return nil }

// approvalProcess returns a process whose stdin a test reads, with one request that
// carries an approval answer and one that carries none.
func approvalProcess(t *testing.T) (*JSONRPCProcess, *agenttest.ControlSink, *agenttest.Stdin) {
	t.Helper()
	output := &agenttest.Stdin{}
	sink := &agenttest.ControlSink{}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
	require.Equal(t, "jsonrpc:1", base.PublishControlRequestWithApproval(sink, []byte(`{"jsonrpc":"2.0","id":1,"method":"approve-me"}`), nil, map[string]any{"decision": "accept"}))
	require.Equal(t, "jsonrpc:2", base.PublishControlRequestWithApproval(sink, []byte(`{"jsonrpc":"2.0","id":2,"method":"ask-me"}`), map[string]any{"action": "cancel"}, nil))
	return base, sink, output
}

func TestPublishControlRequestWithApprovalRegistersAndPublishes(t *testing.T) {
	t.Parallel()
	base, sink, _ := approvalProcess(t)
	assert.Len(t, sink.PublishedControls(), 2)
	assert.True(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.True(t, base.OutstandingControlForTest("jsonrpc:2"))

	failing := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	assert.Empty(t, base.PublishControlRequestWithApproval(failing, []byte(`{"jsonrpc":"2.0","id":3,"method":"approve-me"}`), nil, map[string]any{}),
		"a failed publication registers nothing")
	assert.Empty(t, base.PublishControlRequestWithApproval(sink, []byte(`{"jsonrpc":"2.0","method":"no-id"}`), nil, map[string]any{}))
}

func TestApproveOutstandingControlRequestsAnswersOnlyTheApprovable(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)

	assert.Equal(t, 1, base.ApproveOutstandingControlRequests(sink))

	require.Eventually(t, func() bool { return output.String() != "" }, 2*time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"decision":"accept"}}`, output.String())
	assert.Equal(t, []string{"jsonrpc:1"}, sink.CanceledControls())
	assert.False(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.True(t, base.OutstandingControlForTest("jsonrpc:2"), "a request without an approval answer still waits")
	assert.Zero(t, base.ApproveOutstandingControlRequests(sink), "a second sweep finds nothing")
}

func TestApproveOutstandingControlRequestAnswersOneRequest(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)

	assert.False(t, base.ApproveOutstandingControlRequest(sink, "jsonrpc:2"), "it carries no approval answer")
	assert.False(t, base.ApproveOutstandingControlRequest(sink, "jsonrpc:9"), "it is not open")
	assert.True(t, base.ApproveOutstandingControlRequest(sink, "jsonrpc:1"))
	assert.False(t, base.ApproveOutstandingControlRequest(sink, "jsonrpc:1"), "it is answered once")

	require.Eventually(t, func() bool { return output.String() != "" }, 2*time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"decision":"accept"}}`, output.String())
	assert.Equal(t, []string{"jsonrpc:1"}, sink.CanceledControls())
}

// An approval counts as a withdrawal for a publication that ran concurrently: the
// publication then retires the card that it stored after the approval took its record.
func TestApproveOutstandingControlRequestCountsAsAWithdrawal(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &approvingControlSink{ControlSink: &agenttest.ControlSink{}}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
	sink.onPublish = func() { base.ApproveOutstandingControlRequests(sink) }

	base.PublishControlRequestWithApproval(sink, []byte(`{"jsonrpc":"2.0","id":1,"method":"approve-me"}`), nil, map[string]any{"decision": "accept"})

	assert.Equal(t, []string{"jsonrpc:1", "jsonrpc:1"}, sink.CanceledControls(),
		"the approval cancels before the card exists, so the publication retires the stored card")
	assert.False(t, base.OutstandingControlForTest("jsonrpc:1"))
}

type approvingControlSink struct {
	*agenttest.ControlSink
	onPublish func()
}

func (s *approvingControlSink) PublishControlRequest(request agent.ControlRequest) error {
	if s.onPublish != nil {
		s.onPublish()
	}
	return s.ControlSink.PublishControlRequest(request)
}

func TestSendRawInputToOutstandingRequestRefusesAClosedRequest(t *testing.T) {
	t.Parallel()
	base, _, output := approvalProcess(t)

	require.NoError(t, base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","id":2,"result":{"action":"accept"}}`), agent.StopContext{}))
	assert.False(t, base.OutstandingControlForTest("jsonrpc:2"))

	err := base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","id":2,"result":{"action":"accept"}}`), agent.StopContext{})
	require.ErrorIs(t, err, ErrControlRequestNotPending)
	err = base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":1,"message":"x"}}`), agent.StopContext{})
	require.ErrorIs(t, err, ErrControlRequestNotPending, "an error response is an answer too")
	assert.Equal(t, 1, strings.Count(output.String(), `"id":2`))

	// A request, a notification and a frame that is not JSON pass to SendRawInput.
	require.NoError(t, base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","id":5,"method":"turn/interrupt"}`), agent.StopContext{}))
	require.NoError(t, base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","method":"initialized"}`), agent.StopContext{}))
	require.NoError(t, base.SendRawInputToOutstandingRequest([]byte(`{"jsonrpc":"2.0","id":null,"result":{}}`), agent.StopContext{}))
	assert.Contains(t, output.String(), `"method":"turn/interrupt"`)
	assert.Contains(t, output.String(), `"method":"initialized"`)
}
