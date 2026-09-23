package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeTurnActive_StartedOpensAndCompletedCloses(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi"}`))
	assert.Equal(t, []bool{true}, sink.TurnActives())

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"toolCallCount":1}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestZCodeTurnActive_BackgroundTurnStillClears(t *testing.T) {
	t.Parallel()

	// A background turn (inputSource set) persists no divider and closes none of
	// the user's spans, so finishZCodeTurn returns early. The clear has to happen
	// BEFORE that early return, or a background turn latches the agent busy for
	// the life of the process.
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputSource":"background"}`))
	require.Equal(t, []bool{true}, sink.TurnActives())

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{}`))

	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, last, "a background turn must publish its clear despite the early return")
}

func TestTurnEndPrecedesTheClear_ZCode(t *testing.T) {
	t.Parallel()

	// ZCode is the one that had it backwards: finishZCodeTurn published the
	// clear early so it would run before the two early returns below it. The
	// clear is deferred instead, which covers those returns AND lands after the
	// divider.
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"toolCallCount":1}`))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestZCodeTurnActive_IssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	agenttest.AssertRisingTurnTokens(t, sink, newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{}))
}
