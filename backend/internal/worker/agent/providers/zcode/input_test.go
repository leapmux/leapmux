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
	answer(`"result":{"accepted":true}`)
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
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"accepted":true}`)
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
