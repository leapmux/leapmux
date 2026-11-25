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
			base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":` + wireID + `,"method":"request"}`)})
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
	base.PublishControlRequest(sink, ControlRequestPublication{Content: payload})
	// A marker completes after every earlier frame in the same stdin queue.
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"marker","result":{}}`, output.String())
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

	base.PublishControlRequest(sink, ControlRequestPublication{Owner: ControlRequestOwner{SessionID: "stated-session"}, Content: []byte(`{"jsonrpc":"2.0","id":1,"method":"request"}`)})
	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":2,"method":"request"}`)})
	base.PublishControlRequest(sink, ControlRequestPublication{Owner: ControlRequestOwner{SessionID: ""}, Content: []byte(`{"jsonrpc":"2.0","id":3,"method":"request"}`)})

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
	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"method":"notification"}`)})
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"marker","result":{}}`, output.String())
	assert.Empty(t, sink.PublishedControls())
}

// A request that states no id that a JSON-RPC answer can echo has no key
// (agent.JSONRPCControlRequestIdentity), so the Worker would refuse every answer
// to it. The publisher registers and publishes nothing for it.
func TestJSONRPCControlPublicationRefusesARequestWithNoValidID(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"jsonrpc":"2.0","method":"request"}`,
		`{"jsonrpc":"2.0","id":null,"method":"request"}`,
		`{"jsonrpc":"2.0","id":{"key":1},"method":"request"}`,
		`{"jsonrpc":"2.0","id":true,"method":"request"}`,
		`{"jsonrpc":"2.0","id":[7],"method":"request"}`,
	} {
		sink := &agenttest.ControlSink{}
		base := JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(&agenttest.Stdin{})}}
		publication := base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(payload), ApproveAnswer: map[string]any{}})
		assert.Empty(t, publication.RequestID, payload)
		assert.False(t, base.ControlPublicationIsCurrent(publication), payload)
		assert.Empty(t, sink.PublishedControls(), payload)
		assert.Zero(t, base.OutstandingControlCountForTest(), payload)
	}
}

func TestJSONRPCControlPublicationHandlesReplyFailure(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	base := JSONRPCProcess{Process: Process{stdin: agenttest.FailingStdin{}}}
	require.NotPanics(t, func() {
		base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"id":7,"method":"request"}`)})
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
	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":7,"method":"request"}`), CancelAnswer: map[string]any{"outcome": "cancelled"}})
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
	go func() {
		done <- base.SendRawInput(sink.Answer(key, []byte(`{"jsonrpc":"2.0","id":7,"result":{}}`)))
	}()
	select {
	case <-stdin.entered:
	case err := <-done:
		t.Fatalf("the answer returned before its write: %v", err)
	}
	assert.False(t, base.WithdrawOutstandingControlRequest(key), "the answer in flight decided the request")
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

			require.ErrorContains(t, base.SendRawInput(sink.Answer(key, []byte(`{"jsonrpc":"2.0","id":7,"result":{}}`))), "the pipe closed")
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
	go func() {
		done <- base.SendRawInput(sink.Answer(key, []byte(`{"jsonrpc":"2.0","id":7,"result":{}}`)))
	}()
	select {
	case <-stdin.entered:
	case err := <-done:
		t.Fatalf("the answer returned before its write: %v", err)
	}
	// On its own goroutine: a withdrawal that still found the record would queue
	// its cancel answer behind the held write, and wait for it.
	withdrawn := make(chan struct{})
	go func() {
		base.WithdrawAllControlRequests()
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

	require.Error(t, base.SendRawInput(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","id":8,"result":{}}`)}))
	assert.True(t, base.OutstandingControlForTest(key), "the open request keeps its record")
	assert.Equal(t, 1, base.OutstandingControlCountForTest(), "the failed answer to an unknown id adds no record")

	require.Error(t, base.SendRawInput(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","method":"notify"}`)}))
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
	require.Equal(t, "jsonrpc:1", base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":1,"method":"approve-me"}`), ApproveAnswer: map[string]any{"decision": "accept"}}).RequestID)
	require.Equal(t, "jsonrpc:2", base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":2,"method":"ask-me"}`), CancelAnswer: map[string]any{"action": "cancel"}}).RequestID)
	return base, sink, output
}

func TestPublishControlRequestWithApprovalRegistersAndPublishes(t *testing.T) {
	t.Parallel()
	base, sink, _ := approvalProcess(t)
	assert.Len(t, sink.PublishedControls(), 2)
	assert.True(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.True(t, base.OutstandingControlForTest("jsonrpc:2"))

	failing := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	assert.Empty(t, base.PublishControlRequest(failing, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":3,"method":"approve-me"}`), ApproveAnswer: map[string]any{}}),
		"a failed publication registers nothing")
	assert.Empty(t, base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","method":"no-id"}`), ApproveAnswer: map[string]any{}}))
}

func TestControlPublicationReleasesTheStoredPayload(t *testing.T) {
	t.Parallel()
	base := &JSONRPCProcess{}
	sink := &agenttest.ControlSink{}
	payload := []byte(`{"id":1,"method":"approve","params":{"command":"` + strings.Repeat("x", 100_000) + `"}}`)
	publication := base.PublishControlRequest(sink, ControlRequestPublication{Content: payload, ApproveAnswer: map[string]any{"decision": "accept"}})
	require.NotEmpty(t, publication.RequestID)
	assert.Equal(t, payload, sink.LastPublishedControl().Payload, "the stored request keeps all bytes")
	assert.True(t, publication.record.payload == nil, "the registry retained %d payload bytes after publication", len(publication.record.payload))
	assert.NotNil(t, publication.record.approveAnswer)
}

func TestApproveOutstandingControlRequestsAnswersOnlyTheApprovable(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)

	assert.Equal(t, 1, base.ApproveOutstandingControlRequests(nil))

	require.Eventually(t, func() bool { return output.String() != "" }, 2*time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"decision":"accept"}}`, output.String())
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, time.Second, time.Millisecond)
	assert.False(t, base.OutstandingControlForTest("jsonrpc:1"))
	assert.True(t, base.OutstandingControlForTest("jsonrpc:2"), "a request without an approval answer still waits")
	assert.Zero(t, base.ApproveOutstandingControlRequests(nil), "a second sweep finds nothing")
}

func TestApproveOutstandingControlRequestAnswersOneRequest(t *testing.T) {
	t.Parallel()
	base, sink, output := approvalProcess(t)

	assert.False(t, base.ApproveOutstandingControlRequest(approvalHandle(base, "jsonrpc:2")), "it carries no approval answer")
	assert.False(t, base.ApproveOutstandingControlRequest(approvalHandle(base, "jsonrpc:9")), "it is not open")
	assert.True(t, base.ApproveOutstandingControlRequest(approvalHandle(base, "jsonrpc:1")))
	assert.False(t, base.ApproveOutstandingControlRequest(approvalHandle(base, "jsonrpc:1")), "it is answered once")

	require.Eventually(t, func() bool { return output.String() != "" }, 2*time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"decision":"accept"}}`, output.String())
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, time.Second, time.Millisecond)
}

func TestStandingApprovalKeepsTheControlAfterDefiniteWriteFailure(t *testing.T) {
	t.Parallel()
	stdin := newGatedStdin(0, errors.New("write failed"))
	base := &JSONRPCProcess{Process: Process{stdin: stdin}}
	sink := &agenttest.ControlSink{}
	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"id":1,"method":"approve"}`), ApproveAnswer: map[string]any{"decision": "accept"}})
	require.True(t, base.ApproveOutstandingControlRequest(approvalHandle(base, "jsonrpc:1")))
	ctx := testutil.DeadlineContext(t)
	select {
	case <-stdin.entered:
	case <-ctx.Done():
		t.Fatal("the approval write did not start")
	}
	assert.Empty(t, sink.CanceledControls(), "an unfinished write cannot retire the card")
	close(stdin.release)
	require.Eventually(t, func() bool {
		base.outstandingMu.Lock()
		defer base.outstandingMu.Unlock()
		record := base.outstandingControls["jsonrpc:1"]
		return record != nil && !record.pending
	}, time.Second, time.Millisecond)
	assert.Empty(t, sink.CanceledControls())
}

func TestControlPublicationFailureDoesNotAnswerATakenRequest(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	base := &JSONRPCProcess{Process: Process{stdin: agenttest.NopStdin(output)}}
	sink := &approvingControlSink{ControlSink: &agenttest.ControlSink{PublicationError: errors.New("store failed")}}
	sink.onPublish = func() { base.ApproveOutstandingControlRequests(nil) }
	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"id":1,"method":"approve"}`), ApproveAnswer: map[string]any{"decision": "accept"}})
	require.NoError(t, base.SendResponse([]byte(`"marker"`), map[string]any{}))
	assert.Equal(t, 1, strings.Count(output.String(), `"id":1`))
	assert.NotContains(t, output.String(), `"error"`)
}

// An approval counts as a withdrawal for a publication that ran concurrently: the
// publication then retires the card that it stored after the approval took its record.
func TestApproveOutstandingControlRequestCountsAsAWithdrawal(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &approvingControlSink{ControlSink: &agenttest.ControlSink{}}
	base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
	sink.onPublish = func() { base.ApproveOutstandingControlRequests(nil) }

	base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":1,"method":"approve-me"}`), ApproveAnswer: map[string]any{"decision": "accept"}})

	require.Eventually(t, func() bool { return len(sink.CanceledControls()) > 0 }, time.Second, time.Millisecond,
		"publication and delivery must retire the stored card in either order")
	assert.False(t, base.OutstandingControlForTest("jsonrpc:1"))
}

func TestWithdrawAndAnswerOutstandingControlRequestsUseDeterministicOrder(t *testing.T) {
	t.Parallel()
	wantIDs := []string{
		"jsonrpc:1", "jsonrpc:10", "jsonrpc:2", "jsonrpc:3", "jsonrpc:4",
		"jsonrpc:5", "jsonrpc:6", "jsonrpc:7", "jsonrpc:8", "jsonrpc:9",
	}
	for _, tc := range []struct {
		name string
		run  func(*JSONRPCProcess)
	}{
		{name: "WithdrawAllControlRequests", run: func(b *JSONRPCProcess) { b.WithdrawAllControlRequests() }},
		{name: "AnswerOutstandingControlRequests", run: func(b *JSONRPCProcess) { b.AnswerOutstandingControlRequests() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{}
			base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
			for _, id := range []string{"10", "2", "9", "1", "8", "3", "7", "4", "6", "5"} {
				base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"request"}`), CancelAnswer: map[string]any{"outcome": "cancelled"}})
			}
			tc.run(base)
			assert.Equal(t, wantIDs, sink.CanceledControls())
		})
	}
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
	base, sink, output := approvalProcess(t)

	require.NoError(t, base.SendRawInputToOutstandingRequest(sink.Answer("jsonrpc:2", []byte(`{"jsonrpc":"2.0","id":2,"result":{"action":"accept"}}`))))
	assert.False(t, base.OutstandingControlForTest("jsonrpc:2"))

	err := base.SendRawInputToOutstandingRequest(sink.Answer("jsonrpc:2", []byte(`{"jsonrpc":"2.0","id":2,"result":{"action":"accept"}}`)))
	require.ErrorIs(t, err, agent.ErrControlRequestNotPending)
	err = base.SendRawInputToOutstandingRequest(sink.Answer("jsonrpc:2", []byte(`{"jsonrpc":"2.0","id":2,"error":{"code":1,"message":"x"}}`)))
	require.ErrorIs(t, err, agent.ErrControlRequestNotPending, "an error response is an answer too")
	assert.Equal(t, 1, strings.Count(output.String(), `"id":2`))

	// A request, a notification and a frame that is not JSON pass to SendRawInput.
	require.NoError(t, base.SendRawInputToOutstandingRequest(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","id":5,"method":"turn/interrupt"}`)}))
	require.NoError(t, base.SendRawInputToOutstandingRequest(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","method":"initialized"}`)}))
	require.NoError(t, base.SendRawInputToOutstandingRequest(agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","id":null,"result":{}}`)}))
	assert.Contains(t, output.String(), `"method":"turn/interrupt"`)
	assert.Contains(t, output.String(), `"method":"initialized"`)
}

// A provider can raise a new request with the wire ID of a request that it
// withdrew (registerControl), and a JSON-RPC answer carries no more than that
// ID. So an answer goes only to the publication that it states
// (agent.RawInput.ControlInstanceID). An answer to the withdrawn publication,
// and an answer that states none, reach nothing and leave the new request
// open, so its own withdrawal still reaches it.
func TestJSONRPCControlAnswerReachesOnlyTheInstanceThatItStates(t *testing.T) {
	t.Parallel()
	for name, send := range map[string]func(*JSONRPCProcess, agent.RawInput) error{
		"SendRawInput":                     (*JSONRPCProcess).SendRawInput,
		"SendRawInputToOutstandingRequest": (*JSONRPCProcess).SendRawInputToOutstandingRequest,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{}
			base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
			request := []byte(`{"jsonrpc":"2.0","id":2,"method":"request"}`)
			base.PublishControlRequest(sink, ControlRequestPublication{Content: request})
			withdrawn := sink.LastPublishedControl()
			base.WithdrawControlRequest(sink, withdrawn.RequestID)
			base.PublishControlRequest(sink, ControlRequestPublication{Content: request})
			current := sink.LastPublishedControl()
			require.Equal(t, withdrawn.RequestID, current.RequestID)
			require.NotEqual(t, withdrawn.InstanceID, current.InstanceID)

			answer := []byte(`{"jsonrpc":"2.0","id":2,"result":{"action":"accept"}}`)
			require.ErrorIs(t, send(base, agent.RawInput{Data: answer, ControlInstanceID: withdrawn.InstanceID}), agent.ErrControlInstanceReplaced)
			require.ErrorIs(t, send(base, agent.RawInput{Data: answer}), agent.ErrControlInstanceUnstated)
			assert.Empty(t, output.String(), "no answer reaches the request that reuses the wire ID")
			assert.True(t, base.OutstandingControlForTest(current.RequestID), "the new request stays open")

			require.NoError(t, send(base, agent.RawInput{Data: answer, ControlInstanceID: current.InstanceID}))
			assert.Equal(t, 1, strings.Count(output.String(), `"id":2`))
			assert.False(t, base.OutstandingControlForTest(current.RequestID), "the answer retires the request it answers")
		})
	}
}

// A request that LeapMux sends, such as the raw interrupt frame of
// SendAgentRawMessage, carries LeapMux's own id, which can equal the id of a
// request that the provider raised. It answers nothing: it reaches the
// provider and leaves the provider's request open.
func TestJSONRPCRequestFrameLeavesAProviderRequestOfItsIDOpen(t *testing.T) {
	t.Parallel()
	for name, send := range map[string]func(*JSONRPCProcess, agent.RawInput) error{
		"SendRawInput":                     (*JSONRPCProcess).SendRawInput,
		"SendRawInputToOutstandingRequest": (*JSONRPCProcess).SendRawInputToOutstandingRequest,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output := &agenttest.Stdin{}
			sink := &agenttest.ControlSink{}
			base := &JSONRPCProcess{Process: Process{agentID: "agent", stdin: agenttest.NopStdin(output)}}
			base.PublishControlRequest(sink, ControlRequestPublication{Content: []byte(`{"jsonrpc":"2.0","id":5,"method":"request"}`)})
			key := sink.LastPublishedControl().RequestID

			require.NoError(t, send(base, agent.RawInput{Data: []byte(`{"jsonrpc":"2.0","id":5,"method":"turn/interrupt","params":{}}`)}))
			assert.Contains(t, output.String(), `"method":"turn/interrupt"`)
			assert.True(t, base.OutstandingControlForTest(key), "the provider's request still waits for its answer")
		})
	}
}

func approvalHandle(base *JSONRPCProcess, requestID string) PublishedControlRequest {
	base.outstandingMu.Lock()
	defer base.outstandingMu.Unlock()
	return PublishedControlRequest{RequestID: requestID, record: base.outstandingControls[requestID]}
}
