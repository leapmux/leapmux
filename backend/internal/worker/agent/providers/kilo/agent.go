package kilo

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// Agent manages a single Kilo ACP process.
type Agent struct {
	opencode.FamilyBase
	steers kiloSteerState
	// usageRefresh wakes refreshUsageLoop. newAgent creates it, and it never
	// changes afterwards.
	usageRefresh chan struct{}
	// swarm is the launch value of Kilo Swarm, and swarmConfigOff states that
	// Kilo's own configuration turns it off. Guarded by Base.Mu.
	swarm          bool
	swarmConfigOff bool
}

// newAgent builds the Kilo agent with the launch value of Kilo Swarm. The
// constructor binds the native session-metadata reader before the daemon
// bridge starts, so every start path reports the session goal that Kilo itself
// holds. Once the daemon answers, it reads Kilo's configuration and the plan
// usage, and it reads the usage again after each turn.
func newAgent(swarm bool) *Agent {
	a := &Agent{usageRefresh: make(chan struct{}, 1), swarm: swarm}
	a.MemberEvents = a.observeKiloGoal
	a.MemberOptionGroups = a.swarmOptionGroups
	a.DaemonReady = func(ctx context.Context) {
		a.requestUsageRefresh()
		// The bridge calls this before it reads events, so the read goes on its own goroutine.
		go a.readSwarmConfig(ctx)
	}
	a.TurnEnded = a.requestUsageRefresh
	return a
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Kilo steers through a second ACP prompt and keeps its late answer as a turn.
// This assertion keeps the capability when the family or Kilo agent changes.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// Kilo stops one subagent and steers a running one through the family base,
// as OpenCode does.
var _ agent.ChildInterrupter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)
