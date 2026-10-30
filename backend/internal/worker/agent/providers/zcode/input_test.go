package zcode

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
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

// zcodeInputWriter lets a test deliver native events before a send's write returns.
type zcodeInputWriter func([]byte) (int, error)

func (write zcodeInputWriter) Write(raw []byte) (int, error) { return write(raw) }

func TestZCodeSendInput_PreservesNativeEventsBeforeTheSenderResumes(t *testing.T) {
	t.Parallel()
	for _, receiptFirst := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("receipt_first_%t_completed_%t", receiptFirst, completed), func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
				a.SetStdinForTest(agenttest.NopStdin(zcodeInputWriter(func(raw []byte) (int, error) {
					var request zcodeSentRequest
					require.NoError(t, json.Unmarshal(raw, &request))
					if request.Method != MethodSessionSend {
						return len(raw), nil
					}
					var input struct {
						InputID string `json:"inputId"`
					}
					require.NoError(t, json.Unmarshal(request.Params, &input))
					reply := func() {
						a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(
							`{"sessionId":"sess-1","accepted":true,"stateRevision":1}`)))
					}
					if receiptFirst {
						reply()
					}
					a.HandleOutput(zcodeTurnEventLine(t, 1, "native-turn", contracts.ZCodeEventTurnStarted,
						`{"turnNumber":1,"messageId":"user-message","inputId":"`+input.InputID+`"}`))
					a.HandleOutput(zcodeTurnEventLine(t, 2, "native-turn", contracts.ZCodeEventModelStreaming,
						`{"assistantMessageId":"reply","kind":"text_delta","delta":"retained answer"}`))
					if completed {
						a.HandleOutput(zcodeTurnEventLine(t, 3, "native-turn", contracts.ZCodeEventTurnCompleted,
							`{"resultType":"success"}`))
					}
					if !receiptFirst {
						reply()
					}
					return len(raw), nil
				})))
				require.NoError(t, a.SendInput("hello", nil))
				if !completed {
					a.Mu.Lock()
					owner := a.turns["native-turn"]
					a.Mu.Unlock()
					require.NotNil(t, owner, "the receipt must preserve the already started native owner")
					a.HandleOutput(zcodeTurnEventLine(t, 3, "native-turn", contracts.ZCodeEventTurnCompleted,
						`{"resultType":"success"}`))
				}
				assert.False(t, a.PublishTurnActive().Active, "a completed input must not gain a phantom owner")
				var texts []string
				var ends int
				for _, row := range sink.Messages() {
					if _, text, _, ok := agenttest.DecodeAssembledMessage(row.Content); ok {
						texts = append(texts, text)
					}
					if row.TurnEnd {
						ends++
					}
				}
				assert.Equal(t, []string{"retained answer"}, texts)
				assert.Equal(t, 1, ends)
			})
		}
	}
}

func TestZCodeSendInput_NewAdmissionDoesNotInheritThePreviousToolCount(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeTurnEventLine(t, 0, "first", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 0, "first", contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"call","toolName":"Bash","input":{"command":"pwd"}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 0, "first", contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"call","toolName":"Bash","result":{"success":true,"content":"/workspace"}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 0, "first", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":2}`)
	require.NoError(t, a.SendInput("second", nil))
	request := waitZCodeRequest(t, stdin, MethodSessionSend)
	var input struct {
		InputID string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(request.Params, &input))
	a.HandleOutput(zcodeTurnEventLine(t, 0, "second", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":2,"inputId":"`+input.InputID+`"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 0, "second", contracts.ZCodeEventTurnCompleted, `{"resultType":"success","toolCallCount":0}`))
	assert.Equal(t, []int{1, 0}, agenttest.TurnToolUseCounts(t, sink.Messages()))
}

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

func TestZCodeSendInput_SameIDReplacementReceiptCannotCreateAnOwner(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	result := make(chan error, 1)
	go func() { result <- a.SendInput("old input", nil) }()
	request := zcodeNthRequest(t, stdin, MethodSessionSend, 1)
	snapshot, ok := a.parseStateSnapshot(json.RawMessage(`{"session":{"sessionId":"sess-1"},"runtime":{"stateRevision":1,"eventSeq":0}}`))
	require.True(t, ok)
	a.adoptCompleteZCodeSessionSnapshot(snapshot, true)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"sessionId":"sess-1","accepted":true,"stateRevision":9}`)))
	require.NoError(t, <-result, "the old receipt still proves delivery to its original lifetime")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, int64(1), a.stateRevision)
	assert.Empty(t, a.turns)
}
