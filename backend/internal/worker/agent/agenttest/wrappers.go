package agenttest

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// NonSteerable wraps start so the agent it starts reports that it cannot
// steer, and its interrupt succeeds at once.
//
// The METHOD SET is the point: the wrapper delegates through the Agent
// INTERFACE, never the concrete type, so SteerInput and SupportsSteering are
// not promoted and the Manager's InputSteerer assertion answers false -- the
// running posture of Cursor and Kilo, which preemption exists for. The stubbed
// Interrupt exists because a mock process never answers the interrupt a real
// provider sends.
func NonSteerable(start agent.StartFunc) agent.StartFunc {
	return func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
		inner, err := start(ctx, opts, sink)
		if err != nil {
			return nil, err
		}
		return nonSteerableAgent{inner}, nil
	}
}

type nonSteerableAgent struct{ agent.Agent }

func (nonSteerableAgent) Interrupt() error { return nil }

// IgnoredStop wraps start so the agent it starts states that an earlier
// stop was accepted and then ignored -- the answer that makes the
// InterruptAgent handler escalate the next press into a process replacement.
// The wrapper delegates through the Agent INTERFACE for the same reason
// NonSteerable does: the escalation probe is discovered by method set,
// and the inner agent's own methods must not be promoted.
func IgnoredStop(start agent.StartFunc) agent.StartFunc {
	return func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
		inner, err := start(ctx, opts, sink)
		if err != nil {
			return nil, err
		}
		return ignoredStopAgent{inner}, nil
	}
}

type ignoredStopAgent struct{ agent.Agent }

func (ignoredStopAgent) Interrupt() error               { return nil }
func (ignoredStopAgent) InterruptEscalationReady() bool { return true }
