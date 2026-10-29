package zcode

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zcodeInputStdin struct {
	requests chan []byte
}

func (w zcodeInputStdin) Write(raw []byte) (int, error) {
	w.requests <- append([]byte(nil), raw...)
	return len(raw), nil
}

func (zcodeInputStdin) Close() error { return nil }

func TestZCodeSendInput_RetriesABusyRefusalOnTheProcessClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer("zcode-send-retry")
	defer trap.Close()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	stdin := zcodeInputStdin{requests: make(chan []byte, 4)}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		Ctx: a.Context(), Clock: clock, Stdin: stdin, ProcessDone: make(chan struct{}),
	})
	result := make(chan error, 1)
	go func() { result <- a.SendInput("guide", nil) }()
	ctx := testutil.DeadlineContext(t)
	answer := func(payload string) {
		t.Helper()
		select {
		case raw := <-stdin.requests:
			var request zcodeSentRequest
			require.NoError(t, json.Unmarshal(raw, &request))
			a.HandleOutput([]byte(`{"id":` + string(request.ID) + `,` + payload + `}`))
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	answer(`"error":{"code":-32010,"message":"a prompt is already running"}`)
	assert.Equal(t, zcodeSendRetryInterval, testutil.WaitForTimer(t, ctx, trap))
	assert.Empty(t, stdin.requests)
	clock.Advance(zcodeSendRetryInterval).MustWait(ctx)
	answer(`"result":{"sessionId":"sess-1","accepted":true,"stateRevision":0}`)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestZCodeSendInputSendsPlainSessionSend(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.Mu.Unlock()
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"session-1","accepted":true,"stateRevision":0}`)
	require.NoError(t, a.SendInput("guide", nil))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "guide", params["content"])
	assert.NotContains(t, params, "requestedDelivery", "the strict session/send schema rejects delivery hints")
}

func TestZCodeSendInputTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag)
	defer trap.Close()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		Ctx: a.Context(), Clock: clock, Stdin: stdin, ProcessDone: make(chan struct{}),
	})
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.SetAPITimeoutForTest(time.Minute)
	a.Mu.Unlock()

	result := make(chan error, 1)
	go func() { result <- a.SendInput("guide", nil) }()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(time.Minute).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestZCodeSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.turnActive = true
	a.Mu.Unlock()
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later turn", nil))
}

// Every send receipt must be the full {sessionId, accepted:true, stateRevision}
// document for THIS session. A receipt that leaves the delivery outcome unproven
// is delivery-uncertain: the worker must not report the input as delivered on the
// strength of a reply the decoder could not make sense of.
func TestZCodeSendInput_MalformedReceiptIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, result string }{
		{"a result that is not an object", `"accepted"`},
		{"a null result", `null`},
		{"an empty object", `{}`},
		{"a missing session id", `{"accepted":true,"stateRevision":5}`},
		{"a missing acceptance", `{"sessionId":"sess-1","stateRevision":5}`},
		{"a null acceptance", `{"sessionId":"sess-1","accepted":null,"stateRevision":5}`},
		{"a missing revision", `{"sessionId":"sess-1","accepted":true}`},
		{"a null revision", `{"sessionId":"sess-1","accepted":true,"stateRevision":null}`},
		{"a negative revision", `{"sessionId":"sess-1","accepted":true,"stateRevision":-1}`},
		{"a session id of the wrong type", `{"sessionId":5,"accepted":true,"stateRevision":5}`},
		{"an acceptance of the wrong type", `{"sessionId":"sess-1","accepted":"yes","stateRevision":5}`},
		{"a revision of the wrong type", `{"sessionId":"sess-1","accepted":true,"stateRevision":"5"}`},
		{"another session's receipt", `{"sessionId":"sess-other","accepted":true,"stateRevision":5}`},
		{"the unknown session placeholder", `{"sessionId":"unknown","accepted":true,"stateRevision":5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSend, tc.result)
			err := a.SendInput("guide", nil)
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Empty(t, a.turns, "an unproven receipt must create no turn owner")
			assert.Zero(t, a.stateRevision, "an unproven receipt must advance no revision")
		})
	}
}

// accepted:false is an explicit rejection: the delivery question has a certain
// answer, so the ordinary error stays even when the receipt carries nothing else.
func TestZCodeSendInput_ExplicitRejectionKeepsTheOrdinaryError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, result string }{
		{"a bare rejection", `{"accepted":false}`},
		{"a full rejection", `{"sessionId":"sess-1","accepted":false,"stateRevision":5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
			answerZCodeRequest(t, a, stdin, MethodSessionSend, tc.result)
			err := a.SendInput("guide", nil)
			require.Error(t, err)
			assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the app-server answered with a verdict")
			assert.ErrorContains(t, err, "did not accept")
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Empty(t, a.turns, "a rejected input owns no turn")
		})
	}
}

// stateRevision 0 is a valid revision on a fresh session. A required zero must
// not be mistaken for an absent field.
func TestZCodeSendInput_AcceptsAZeroStateRevision(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":0}`)
	require.NoError(t, a.SendInput("guide", nil))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Len(t, a.turns, 1, "a valid zero-revision receipt still creates the provisional owner")
	assert.Zero(t, a.stateRevision)
}

// A valid receipt folds its revision into the session state monotonically: the
// counter rises to the receipt and never falls back.
func TestZCodeSendInput_FoldsTheReceiptRevisionMonotonically(t *testing.T) {
	t.Parallel()

	t.Run("raises the session revision", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.stateRevision = 3
		a.Mu.Unlock()
		answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":7}`)
		require.NoError(t, a.SendInput("guide", nil))

		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Equal(t, int64(7), a.stateRevision)
	})

	t.Run("keeps a higher observed revision", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.stateRevision = 10
		a.Mu.Unlock()
		answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":7}`)
		require.NoError(t, a.SendInput("guide", nil))

		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Equal(t, int64(10), a.stateRevision, "a stale receipt must not move the counter back")
	})
}

// A send that crosses a session replacement belongs to the replaced session.
// Its receipt is valid, but it must leave no mark on the replacement: no
// revision advance and no provisional turn owner.
func TestZCodeSendInput_ReplacedSessionReceiptDoesNotTouchTheNewSession(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.stateRevision = 3
	a.Mu.Unlock()

	result := make(chan error, 1)
	go func() { result <- a.SendInput("guide", nil) }()
	ctx := testutil.DeadlineContext(t)
	request := waitZCodeRequest(t, stdin, MethodSessionSend)

	// ClearContext replaces the session while the send is in flight.
	a.Mu.Lock()
	a.sessionID = "sess-fresh"
	a.stateRevision = 3
	a.Mu.Unlock()

	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request),
		json.RawMessage(`{"sessionId":"sess-1","accepted":true,"stateRevision":9}`)))

	select {
	case err := <-result:
		require.NoError(t, err, "the receipt proves delivery to the replaced session")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, int64(3), a.stateRevision, "the replaced session's receipt must not advance the replacement's revision")
	assert.Empty(t, a.turns, "the replaced session's receipt must create no turn owner on the replacement")
	assert.Empty(t, a.foregroundTurnID)
}
