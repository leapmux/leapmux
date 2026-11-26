package kilo

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// Agent manages a single Kilo ACP process.
type Agent struct {
	opencode.FamilyBase
	steers          kiloSteerState
	nativeReplies   kiloNativeReplies
	daemonReady     chan struct{}
	daemonReadyOnce sync.Once
	daemonFailed    atomic.Bool
	workingDir      string
	// usageRefresh wakes refreshUsageLoop. newAgent creates it, and it never
	// changes afterwards.
	usageRefresh chan struct{}
	// swarm is the launch value of Kilo Swarm, and swarmConfigOff states that
	// Kilo's own configuration turns it off. Guarded by Base.Mu.
	swarm          bool
	swarmConfigOff bool
}

func (a *Agent) markDaemonReady() {
	a.daemonReadyOnce.Do(func() {
		close(a.daemonReady)
	})
}

// newAgent builds the Kilo agent with the launch value of Kilo Swarm. The
// constructor binds the native session-metadata reader before the daemon
// bridge starts, so every start path reports the session goal that Kilo itself
// holds. Once the daemon answers, it reads Kilo's configuration and the plan
// usage, and it reads the usage again after each turn.
func newAgent(swarm bool) *Agent {
	a := &Agent{usageRefresh: make(chan struct{}, 1), daemonReady: make(chan struct{}), swarm: swarm}
	a.MemberEvents = a.observeKiloGoal
	a.ConfigureHooks = a.configureHooks
	a.QuestionTurnID = a.nativeQuestionTurnID
	a.MemberOptionGroups = a.swarmOptionGroups
	a.DaemonReady = func(ctx context.Context) {
		a.markDaemonReady()
		a.requestUsageRefresh()
		// The bridge calls this before it reads events, so the read goes on its own goroutine.
		go a.readSwarmConfig(ctx)
	}
	a.DaemonUnavailable = func() {
		a.daemonFailed.Store(true)
		a.markDaemonReady()
	}
	a.TurnEnded = a.requestUsageRefresh
	return a
}

func (a *Agent) configureHooks(hooks *acp.Hooks) {
	hooks.AssistantTextKey = kiloAssistantTextKey
	hooks.AssistantTextPrepared = a.nativeReplies.recordText
	hooks.RootPromptTransport = a.prepareNativePrompt
	baseControlOwner := hooks.ControlOwner
	hooks.ControlOwner = func(sessionID string, line *providerkit.ParsedLine, needsTurn bool) (acp.ControlOwnerResolution, error) {
		var res acp.ControlOwnerResolution
		if baseControlOwner != nil {
			var err error
			res, err = baseControlOwner(sessionID, line, needsTurn)
			if err != nil || res.Refuse {
				return res, err
			}
		}
		if needsTurn && res.TurnID == "" && line.Method == acp.MethodSessionRequestPermission {
			params, _ := providerkit.RawObject(line.Params)
			toolCallID := providerkit.StringField(params["toolCall"], contracts.ACPSupplementIdentityToolCallID)
			if toolCallID != "" {
				effectiveSession := res.SessionID
				if effectiveSession == "" {
					effectiveSession = sessionID
				}
				res.TurnID = a.nativePermissionTurnID(effectiveSession, toolCallID)
			}
		}
		return res, nil
	}
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Kilo delivers a steer as a separate prompt and preserves its late answer.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// Kilo can interrupt or steer a running subagent on its own native session.
var _ agent.ChildInterrupter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)
