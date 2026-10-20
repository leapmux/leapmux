package junie

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one Junie ACP process.
type Agent struct {
	acp.Base
	// launchEffortOverride is the stored effort passed through --effort at start.
	// An absent override differs from the default level Junie reports in ACP.
	launchEffortOverride string
	// homeDir is the fallback when JUNIE_HOME does not select the native store.
	homeDir string
	// workingDir links a native session summary to this agent's project.
	workingDir string
	// clock drives reads of the native child snapshots while a child runs.
	clock quartz.Clock
	// childTailMu guards the readers keyed by their ACP child session IDs.
	childTailMu sync.Mutex
	childTails  map[string]*junieChildTail

	// goalMu guards the goal-report bookkeeping. It is separate from Base.Mu so
	// the goal fold never takes the base lock.
	goalMu sync.Mutex
	// goalStartedAt is when this process began. A goal whose createdAt
	// predates it was stored by an earlier run and its first report is a
	// restatement.
	goalStartedAt time.Time
	// goalLast is the fingerprint of the last goal report, and nil when the
	// goal is absent. It separates a restatement from a transition.
	goalLast *junieGoalFingerprint
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers by a queued prompt of its own. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying
// InputSteerer, so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

// junieSteerMethod is Junie's own steering method: it injects into the running
// turn without refusing the concurrent `session/prompt` that a plain follow-up
// prompt raises.
const junieSteerMethod = "_session/steering"

// SteerInput injects text into the running turn through Junie's own
// `_session/steering`. The reply's outcome states what the session did:
// `injected` reached the running turn, and `startedNewTurn` began a new turn of
// Junie's own, which the provider tracks as an agent turn so its end still
// closes the turn state. A refused request answers ErrNoActiveTurn, so the
// queue keeps the input for the next turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if content == "" {
		return nil
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	result, err := a.SendSessionRPCObserved(junieSteerMethod, map[string]interface{}{
		"prompt": acp.BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	}, func(response json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		var reply struct {
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(response, &reply) != nil {
			return
		}
		// A steer that started a new turn of Junie's own runs outside the
		// prompt of LeapMux: track it as an agent turn so the turn state and
		// the reader follow it to its own end.
		if reply.Outcome == "startedNewTurn" {
			a.BeginAgentTurn()
		}
	})
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError(junieSteerMethod, err)
	}
	_ = result
	return nil
}
