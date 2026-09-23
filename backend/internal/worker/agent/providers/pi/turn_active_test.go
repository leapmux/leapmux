package pi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiTurnActive_StaysOpenAcrossAnAutoRetry(t *testing.T) {
	t.Parallel()

	// Pi restarts a failed run itself. The turn stays open for the whole
	// backoff, where nothing streams and no envelope arrives -- and a client
	// that inferred idleness there would drop the spinner and hide the Interrupt
	// button on a run that is not finished.
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":true}`)))

	last, _ := sink.LastTurnActive()
	assert.True(t, last, "an agent_end that will retry does not end the turn")

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	last, _ = sink.LastTurnActive()
	assert.False(t, last, "the retry budget is spent; now the turn ends")
}

func TestTurnEndPrecedesTheClear_Pi(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

// Pi keeps the turn open across a retry it drives itself, so the retried
// attempt persists a plain message rather than a turn end -- and publishes no
// clear for the settle to spend a count on.
func TestTurnEndPrecedesTheClear_PiRetryEndsNoTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":true}`)))

	// The republished `true` is a re-read of an unchanged flag, not a second
	// turn. It is harmless because the Worker's latch is edge-triggered, and it
	// is the price of publishing from the field rather than from an argument --
	// which is what makes a MISSING publish the only way the two can drift.
	assert.Equal(t, []string{"turn_active:true", "reset_spans", "turn_active:true"}, sink.TurnLifecycle(),
		"a retried attempt neither ends the turn nor clears the flag")

	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{
		"turn_active:true", "reset_spans", "turn_active:true", "turn_end", "reset_spans", "turn_active:false",
	}, sink.TurnLifecycle())
}

func TestPiTurnActive_IssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	agenttest.AssertRisingTurnTokens(t, sink, newPiAgentWithSink(agent.NewProviderServices(sink)))
}
