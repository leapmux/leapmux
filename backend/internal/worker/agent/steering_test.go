package agent_test

import (
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// steeringStub is an InputSteerer whose SupportsSteering answer the test
// controls. It embeds IdleAgent (manager_startup_concurrency_test.go) for the
// rest of the Agent surface, because manager_test.go's stubProvider is
// `//go:build unix` and this case has no platform behaviour.
type steeringStub struct {
	agenttest.IdleAgent
	supports bool
}

func (s *steeringStub) SteerInput(string, []*leapmuxv1.Attachment) error { return nil }
func (s *steeringStub) SupportsSteering() bool                           { return s.supports }

// Manager.SupportsSteering asks the provider and reports the answer. It never
// assumes true for a provider that implements SteerInput, because the Manager
// publishes this answer to the client, which shows or hides the Steer control.
func TestManagerSupportsSteeringAsksTheProvider(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("refuses", &steeringStub{})
	m.PutAgentForTest("accepts", &steeringStub{supports: true})
	m.PutAgentForTest("plain", agenttest.IdleAgent{})

	assert.False(t, m.SupportsSteering("refuses"),
		"a provider that answers false must not claim the capability")
	assert.True(t, m.SupportsSteering("accepts"))
	assert.False(t, m.SupportsSteering("plain"),
		"a provider that does not implement SteerInput cannot steer")
	assert.False(t, m.SupportsSteering("unknown-agent"))
}

// busyProvider refuses every send the way a provider inside its own turn does,
// and counts the republishes the Manager asks for. It embeds IdleAgent for the
// rest of the Agent surface, for the same reason steeringStub does.
type busyProvider struct {
	agenttest.IdleAgent
	refuse    error
	republish int
	turnState agent.TurnState
	supports  bool
}

func (p *busyProvider) SendInput(string, []*leapmuxv1.Attachment) error { return p.refuse }
func (p *busyProvider) SteerInput(string, []*leapmuxv1.Attachment) error {
	return nil
}
func (p *busyProvider) SupportsSteering() bool { return p.supports }
func (p *busyProvider) PublishTurnActive() agent.TurnState {
	p.republish++
	return p.turnState
}

func TestManagerSendInputRepublishesTheTurnARefusalDisproves(t *testing.T) {
	t.Parallel()

	// A busy refusal is PROOF that the Worker's view of the turn was wrong: it
	// dispatched into a turn the provider was already running. Both consumers of
	// the flag -- the activity state and the input queue's dispatch guard -- are
	// wrong at that moment, and this is where they are repaired. Doing it here
	// rather than in each provider is what stops a sixth provider from leaving
	// it out.
	for _, tc := range []struct {
		name          string
		refuse        error
		republish     int
		turnState     agent.TurnState
		supports      bool
		wantSteerable bool
	}{
		{
			name: "busy steering provider", refuse: fmt.Errorf("send: %w", agent.ErrAgentBusy), republish: 1,
			turnState: agent.TurnState{Active: true, Steerable: true}, supports: true, wantSteerable: true,
		},
		{
			name: "busy classified compaction", refuse: fmt.Errorf("send: %w", agent.ErrAgentBusy), republish: 1,
			turnState: agent.TurnState{Active: true}, supports: true,
		},
		{
			name: "busy non-steering provider", refuse: fmt.Errorf("send: %w", agent.ErrAgentBusy), republish: 1,
			turnState: agent.TurnState{Active: true},
		},
		{name: "delivered", refuse: nil, republish: 0},
		{name: "other failure", refuse: errors.New("broken pipe"), republish: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &busyProvider{refuse: tc.refuse, turnState: tc.turnState, supports: tc.supports}
			m := agent.NewManager(testRegistry, nil)
			m.PutAgentForTest("agent-1", provider)

			err := m.SendInput("agent-1", "later turn", nil)
			assert.Equal(t, tc.refuse != nil, err != nil)
			assert.Equal(t, tc.republish, provider.republish,
				"only a busy refusal disproves the Worker's view of the turn")
			if tc.republish > 0 {
				var busyErr *agent.AgentBusyError
				require.ErrorAs(t, err, &busyErr)
				assert.Equal(t, tc.wantSteerable, busyErr.ActiveTurnSteerable)
			}
		})
	}
}
