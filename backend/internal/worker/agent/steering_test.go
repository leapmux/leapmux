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
// controls. It embeds agenttest.IdleAgent (agenttest/agent_stubs.go) for the
// rest of the Agent surface, because manager_test.go's stubProvider is
// `//go:build unix` and this case has no platform behaviour.
type steeringStub struct {
	agenttest.IdleAgent
	supports bool
}

func (s *steeringStub) SteerInput(agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return agent.NativeInputReceipt{}, nil
}
func (s *steeringStub) SupportsSteering() bool { return s.supports }

// The assertion keeps the stub an InputSteerer when the interface changes. A
// stub that stopped being one would make the capability test pass for the
// wrong reason.
var _ agent.InputSteerer = (*steeringStub)(nil)

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

func (p *busyProvider) SendInput(string, []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return agent.NativeInputReceipt{}, p.refuse
}
func (p *busyProvider) SteerInput(agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return agent.NativeInputReceipt{}, nil
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

			_, err := m.SendInput("agent-1", "later turn", nil)
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

// steerRecorder records each steer that reaches the provider, and the session
// of each session-checked steer.
type steerRecorder struct {
	agenttest.IdleAgent
	sendReceipt  agent.NativeInputReceipt
	steerReceipt agent.NativeInputReceipt
	requests     []agent.SteerRequest
	sessions     []string
}

func (r *steerRecorder) SendInput(string, []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return r.sendReceipt, nil
}

func (r *steerRecorder) SendInputForSession(sessionID, _ string, _ []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	r.sessions = append(r.sessions, sessionID)
	return r.sendReceipt, nil
}

func (r *steerRecorder) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	r.requests = append(r.requests, request)
	return r.steerReceipt, nil
}

func (r *steerRecorder) SteerInputForSession(sessionID string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	r.sessions = append(r.sessions, sessionID)
	return r.SteerInput(request)
}

func (*steerRecorder) SupportsSteering() bool { return true }

var _ agent.SessionInputSteerer = (*steerRecorder)(nil)

// A provider that drops a steer hands back the request it received, so the
// request must reach it whole: the queue item first of all.
func TestManagerSteerInputHandsTheProviderTheWholeRequest(t *testing.T) {
	t.Parallel()

	wantSend := agent.NativeInputReceipt{SessionID: "session-1", InputID: "send-1", TurnID: "turn-1"}
	wantSteer := agent.NativeInputReceipt{SessionID: "session-1", InputID: "steer-1", TurnID: "turn-1"}
	provider := &steerRecorder{sendReceipt: wantSend, steerReceipt: wantSteer}
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("agent-1", provider)
	request := agent.SteerRequest{
		InputID: "input-1", Content: "Also check the tests.",
		Attachments: []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("body")}},
	}

	gotSend, err := m.SendInput("agent-1", "Start a turn.", nil)
	require.NoError(t, err)
	assert.Equal(t, wantSend, gotSend)

	gotSessionSend, err := agent.SendInputToSession(provider, "session-1", "Follow up.", nil)
	require.NoError(t, err)
	assert.Equal(t, wantSend, gotSessionSend)

	receipt, err := m.SteerInput("agent-1", request)
	require.NoError(t, err)
	assert.Equal(t, wantSteer, receipt)
	assert.Equal(t, []agent.SteerRequest{request}, provider.requests)

	sessionReceipt, err := agent.SteerInputToSession(provider, "session-1", request)
	require.NoError(t, err)
	assert.Equal(t, wantSteer, sessionReceipt)
	assert.Equal(t, []string{"session-1", "session-1"}, provider.sessions)
	assert.Equal(t, []agent.SteerRequest{request, request}, provider.requests)
}

// A steer without its queue item could not come back if the provider dropped
// it, so both steer paths refuse it before a provider reads it.
func TestSteerRefusesARequestWithoutAQueueItem(t *testing.T) {
	t.Parallel()

	provider := &steerRecorder{}
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("agent-1", provider)
	request := agent.SteerRequest{Content: "Also check the tests."}

	_, err := m.SteerInput("agent-1", request)
	require.ErrorContains(t, err, "states no queue item")
	_, err = agent.SteerInputToSession(provider, "session-1", request)
	require.ErrorContains(t, err, "states no queue item")
	assert.Empty(t, provider.requests)
	assert.Empty(t, provider.sessions)
}
