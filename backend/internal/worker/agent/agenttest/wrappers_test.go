package agenttest

import (
	"context"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errInnerInterrupt is what capableAgent.Interrupt returns, so a test can tell
// the stubbed interrupt of a wrapper from the interrupt of the inner agent.
var errInnerInterrupt = errors.New("inner interrupt")

// capableAgent has every capability that a wrapper must hide: it steers, its
// interrupt fails, and it states that an escalation is not ready.
type capableAgent struct{ IdleAgent }

func (capableAgent) AgentID() string                                  { return "inner" }
func (capableAgent) Interrupt() error                                 { return errInnerInterrupt }
func (capableAgent) SteerInput(string, []*leapmuxv1.Attachment) error { return nil }
func (capableAgent) SupportsSteering() bool                           { return true }
func (capableAgent) InterruptEscalationReady() bool                   { return false }

// escalationProbe is the method set that Manager.InterruptEscalationReady looks for.
type escalationProbe interface{ InterruptEscalationReady() bool }

// The inner agent has both capabilities, so a wrapper that answers false proves
// that it hides them.
var (
	_ agent.InputSteerer = capableAgent{}
	_ escalationProbe    = capableAgent{}
)

// startCapable starts a capableAgent.
func startCapable(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
	return capableAgent{}, nil
}

func TestNonSteerableHidesTheSteeringOfTheInnerAgent(t *testing.T) {
	t.Parallel()

	a, err := NonSteerable(startCapable)(context.Background(), agent.Options{}, nil)
	require.NoError(t, err)

	_, steers := a.(agent.InputSteerer)
	assert.False(t, steers, "the wrapper must not promote SteerInput or SupportsSteering")
	_, probes := a.(escalationProbe)
	assert.False(t, probes, "the wrapper must not promote the escalation probe")
	assert.NoError(t, a.Interrupt(), "the interrupt succeeds at once")
	assert.Equal(t, "inner", a.AgentID(), "every other method reaches the inner agent")
}

func TestIgnoredStopStatesThatTheEscalationIsReady(t *testing.T) {
	t.Parallel()

	a, err := IgnoredStop(startCapable)(context.Background(), agent.Options{}, nil)
	require.NoError(t, err)

	probe, probes := a.(escalationProbe)
	require.True(t, probes, "the Manager finds the escalation probe by method set")
	assert.True(t, probe.InterruptEscalationReady(), "the wrapper answers, not the inner agent")
	_, steers := a.(agent.InputSteerer)
	assert.False(t, steers, "the wrapper must not promote SteerInput or SupportsSteering")
	assert.NoError(t, a.Interrupt(), "the interrupt succeeds at once")
	assert.Equal(t, "inner", a.AgentID(), "every other method reaches the inner agent")
}

// A start that fails returns its own error and no agent, so the Manager never
// registers a wrapper around nothing.
func TestWrappersPassAStartErrorThrough(t *testing.T) {
	t.Parallel()

	errStart := errors.New("start failed")
	failing := func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
		return nil, errStart
	}
	for name, wrap := range map[string]func(agent.StartFunc) agent.StartFunc{
		"NonSteerable": NonSteerable,
		"IgnoredStop":  IgnoredStop,
	} {
		t.Run(name, func(t *testing.T) {
			a, err := wrap(failing)(context.Background(), agent.Options{}, nil)
			assert.ErrorIs(t, err, errStart)
			assert.Nil(t, a)
		})
	}
}
