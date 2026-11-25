package junie

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/quartz"
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
	clock           quartz.Clock
	steerDispatchMu sync.Mutex
	steerReplies    junieSteerReplies
	// childTailMu guards childTails, openChildren, and finishedChildren keyed
	// by their ACP child session IDs.
	childTailMu      sync.Mutex
	childTails       map[string]*junieChildTail
	openChildren     map[string]struct{}
	finishedChildren map[string]struct{}

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

// observeJunieTurnEnd reads the `_meta.lastRequestComplete` marker that ends a
// turn Junie started by itself: a steer whose reply said `startedNewTurn`
// began it (see SteerInput), and the marked empty chunk is that turn's only
// end, so the agent turn ends there with no turn-end row. A turn LeapMux
// started ends at its session/prompt response, where ending no agent turn is a
// no-op.
func (a *Agent) observeJunieTurnEnd(_ string, metadata map[string]json.RawMessage) {
	if providerkit.RawBool(metadata["lastRequestComplete"]) {
		a.EndAgentTurnWithoutRow()
	}
}
