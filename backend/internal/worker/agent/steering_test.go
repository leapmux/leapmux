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
	steerErr     error
	prepareErr   error
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
	return r.steerReceipt, r.steerErr
}

func (r *steerRecorder) SteerInputForSession(sessionID string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	r.sessions = append(r.sessions, sessionID)
	return r.SteerInput(request)
}

func (*steerRecorder) SupportsSteering() bool { return true }

func (r *steerRecorder) PrepareInputProcess() error { return r.prepareErr }

func (*steerRecorder) InputProcessRecoveryRequired() bool { return false }

var (
	_ agent.SessionInputSteerer  = (*steerRecorder)(nil)
	_ agent.InputProcessPreparer = (*steerRecorder)(nil)
)

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

type observedGoalCommand struct {
	delivery agent.GoalCommandDelivery
	text     string
}

type rowAtEchoSteerStub struct {
	steerRecorder
	rowAtEcho     bool
	observedGoals []observedGoalCommand
}

func (s *rowAtEchoSteerStub) SteerRowAtEcho() bool { return s.rowAtEcho }

func (s *rowAtEchoSteerStub) SendChildInput(string, agent.ChildInput) (agent.ChildInputResult, error) {
	return agent.ChildInputResult{}, nil
}

func (s *rowAtEchoSteerStub) SteerChildInput(_ string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	return s.SteerInput(request)
}

func (s *rowAtEchoSteerStub) ChildSteerPolicy() agent.SteerPolicy {
	return agent.SteerPolicy{RowAtEcho: s.rowAtEcho}
}

func (*rowAtEchoSteerStub) ActiveChildTurnState(string) agent.TurnState { return agent.TurnState{} }

func (*rowAtEchoSteerStub) SupportedGoalActions() []agent.GoalAction { return nil }

func (*rowAtEchoSteerStub) PerformGoalAction(agent.GoalAction, string) (agent.GoalOutcome, error) {
	return agent.GoalOutcome{}, nil
}

func (s *rowAtEchoSteerStub) ObserveGoalCommand(delivery agent.GoalCommandDelivery, text string) {
	s.observedGoals = append(s.observedGoals, observedGoalCommand{delivery: delivery, text: text})
}

var (
	_ agent.ChildSteerer      = (*rowAtEchoSteerStub)(nil)
	_ agent.SteerEchoReporter = (*rowAtEchoSteerStub)(nil)
	_ agent.GoalTextCommander = (*rowAtEchoSteerStub)(nil)
)

func TestSteerRejectsSynchronousReceiptWhenRowAtEcho(t *testing.T) {
	t.Parallel()
	for name, badReceipt := range map[string]agent.NativeInputReceipt{
		"session and input": {SessionID: "s-1", InputID: "i-1"},
		"session only":      {SessionID: "s-1"},
		"input only":        {InputID: "i-1"},
		"turn only":         {TurnID: "t-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := &rowAtEchoSteerStub{
				steerRecorder: steerRecorder{steerReceipt: badReceipt},
				rowAtEcho:     true,
			}
			m := agent.NewManager(testRegistry, nil)
			m.PutAgentForTest("agent-1", provider)
			req := agent.SteerRequest{InputID: "input-1", Content: "steer"}

			receipt, err := m.SteerInput("agent-1", req)
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			assert.Empty(t, receipt)

			receipt, err = agent.SteerInputToSession(provider, "s-1", req)
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			assert.Empty(t, receipt)

			receipt, err = m.SteerChildInput("agent-1", "child-1", req)
			require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			assert.Empty(t, receipt)

			assert.Empty(t, provider.observedGoals, "rejected steers must not observe goal commands")
		})
	}
}

func TestSteerReceiptValidationHappyPathsAndGoalObservation(t *testing.T) {
	t.Parallel()

	t.Run("RowAtEcho true accepts empty receipt and observes goal command", func(t *testing.T) {
		t.Parallel()
		provider := &rowAtEchoSteerStub{rowAtEcho: true}
		m := agent.NewManager(testRegistry, nil)
		m.PutAgentForTest("agent-1", provider)
		req := agent.SteerRequest{InputID: "input-1", Content: "/goal ship"}

		receipt, err := m.SteerInput("agent-1", req)
		require.NoError(t, err)
		assert.Empty(t, receipt)

		receipt, err = agent.SteerInputToSession(provider, "s-1", req)
		require.NoError(t, err)
		assert.Empty(t, receipt)

		receipt, err = m.SteerChildInput("agent-1", "child-1", req)
		require.NoError(t, err)
		assert.Empty(t, receipt)

		assert.Equal(t, []observedGoalCommand{
			{delivery: agent.GoalDeliverySteer, text: "/goal ship"},
			{delivery: agent.GoalDeliverySteer, text: "/goal ship"},
		}, provider.observedGoals)
	})

	t.Run("RowAtEcho false preserves non-empty receipt across all steer paths", func(t *testing.T) {
		t.Parallel()
		want := agent.NativeInputReceipt{SessionID: "s-1", InputID: "i-1", TurnID: "t-1"}
		provider := &rowAtEchoSteerStub{
			steerRecorder: steerRecorder{steerReceipt: want},
			rowAtEcho:     false,
		}
		m := agent.NewManager(testRegistry, nil)
		m.PutAgentForTest("agent-1", provider)
		req := agent.SteerRequest{InputID: "input-1", Content: "/goal keep"}

		receipt, err := m.SteerInput("agent-1", req)
		require.NoError(t, err)
		assert.Equal(t, want, receipt)

		receipt, err = agent.SteerInputToSession(provider, "s-1", req)
		require.NoError(t, err)
		assert.Equal(t, want, receipt)

		receipt, err = m.SteerChildInput("agent-1", "child-1", req)
		require.NoError(t, err)
		assert.Equal(t, want, receipt)

		assert.Equal(t, []observedGoalCommand{
			{delivery: agent.GoalDeliverySteer, text: "/goal keep"},
			{delivery: agent.GoalDeliverySteer, text: "/goal keep"},
		}, provider.observedGoals)
	})
}

func TestSteerProviderErrorTakesPrecedenceOverRowAtEchoReceipt(t *testing.T) {
	t.Parallel()
	provider := &rowAtEchoSteerStub{
		steerRecorder: steerRecorder{
			steerReceipt: agent.NativeInputReceipt{SessionID: "s-1", InputID: "i-1", TurnID: "t-1"},
			steerErr:     agent.ErrNoActiveTurn,
		},
		rowAtEcho: true,
	}
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("agent-1", provider)
	req := agent.SteerRequest{InputID: "input-1", Content: "steer"}

	_, err := m.SteerInput("agent-1", req)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

	_, err = agent.SteerInputToSession(provider, "s-1", req)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

	_, err = m.SteerChildInput("agent-1", "child-1", req)
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

	assert.Empty(t, provider.observedGoals)
}

func TestSteerErrorPaths(t *testing.T) {
	t.Parallel()
	req := agent.SteerRequest{InputID: "input-1", Content: "steer"}

	t.Run("unknown agent", func(t *testing.T) {
		t.Parallel()
		m := agent.NewManager(testRegistry, nil)
		receipt, err := m.SteerInput("missing", req)
		require.ErrorIs(t, err, agent.ErrAgentNotFound)
		assert.Empty(t, receipt)

		receipt, err = m.SteerChildInput("missing", "child-1", req)
		require.ErrorIs(t, err, agent.ErrAgentNotFound)
		assert.Empty(t, receipt)
	})

	t.Run("unsupported provider interfaces", func(t *testing.T) {
		t.Parallel()
		m := agent.NewManager(testRegistry, nil)
		m.PutAgentForTest("plain", agenttest.IdleAgent{})
		m.PutAgentForTest("root-only", &steeringStub{supports: true})

		receipt, err := m.SteerInput("plain", req)
		require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
		assert.Empty(t, receipt)

		receipt, err = agent.SteerInputToSession(&steeringStub{supports: true}, "s-1", req)
		require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
		assert.Empty(t, receipt)

		receipt, err = m.SteerChildInput("root-only", "child-1", req)
		require.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
		assert.Empty(t, receipt)
	})

	t.Run("PrepareInputProcess error stops steer before provider call", func(t *testing.T) {
		t.Parallel()
		prepareErr := errors.New("process restart required")
		provider := &rowAtEchoSteerStub{
			steerRecorder: steerRecorder{prepareErr: prepareErr},
		}
		m := agent.NewManager(testRegistry, nil)
		m.PutAgentForTest("agent-1", provider)

		receipt, err := m.SteerInput("agent-1", req)
		require.ErrorIs(t, err, prepareErr)
		assert.Empty(t, receipt)
		assert.Empty(t, provider.requests)
		assert.Empty(t, provider.observedGoals)
	})
}
