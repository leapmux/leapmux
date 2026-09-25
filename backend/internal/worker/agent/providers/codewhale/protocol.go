package codewhale

import (
	"regexp"
	"strings"
)

// Codewhale runtime API vocabulary that only the worker spells.
//
// The values that the browser plugin reads too are generated from
// contracts/codewhale-protocol.json. This file holds the rest: the REST routes,
// the events that the worker consumes and never persists, and the process
// environment that the worker sets.

// codewhaleProviderName identifies the provider in logs and in errors.
const codewhaleProviderName = "codewhale"

// Runtime API routes. A path parameter is appended with url.PathEscape, never by
// string formatting of a raw value.
const (
	routeHealth      = "/health"
	routeRuntimeInfo = "/v1/runtime/info"
	routeThreads     = "/v1/threads"
	routeApprovals   = "/v1/approvals"
	routeUserInput   = "/v1/user-input"
	routeProviders   = "/v1/providers"
	routeAgentRuns   = "/v1/agent-runs"

	// Thread sub-routes, after /v1/threads/{id}.
	threadRouteResume  = "/resume"
	threadRouteEvents  = "/events"
	threadRouteTurns   = "/turns"
	threadRouteCompact = "/compact"
	threadRouteGoal    = "/goal"
	// threadRouteContext exists from 0.10.0. start.go probes it, because the
	// runtime capability map does not report route additions.
	threadRouteContext = "/context"
	// threadRouteJobs exists from 0.10.0. It lists the thread's background
	// shell jobs, and `/jobs/{job_id}` below it reads one. 0.9.13 answers 404,
	// which the shell poller reads as "no such route". See subagent.go.
	threadRouteJobs = "/jobs"

	// Turn sub-routes, after /v1/threads/{id}/turns/{turn_id}.
	turnRouteSteer     = "/steer"
	turnRouteInterrupt = "/interrupt"

	// providerRouteModels follows /v1/providers/{id}.
	providerRouteModels = "/models"

	// eventsQuerySinceSeq resumes the event stream after a sequence number. The
	// server subscribes before it replays, so no event falls between the replay
	// and the live stream.
	eventsQuerySinceSeq = "since_seq"
)

// Runtime events that the worker consumes and never persists. The persisted
// ones, which the browser plugin classifies, are in the contract.
const (
	eventThreadStarted          = "thread.started"
	eventThreadUpdated          = "thread.updated"
	eventThreadForked           = "thread.forked"
	eventTurnStarted            = "turn.started"
	eventTurnLifecycle          = "turn.lifecycle"
	eventTurnUsage              = "turn.usage"
	eventTurnSteered            = "turn.steered"
	eventTurnInterruptRequested = "turn.interrupt_requested"
	eventItemDelta              = "item.delta"
	eventApprovalDecided        = "approval.decided"
	eventUserInputAnswered      = "user_input.answered"
	eventGoalUpdated            = "thread_goal_updated"
	eventGoalCleared            = "thread_goal_cleared"
	eventModelToolsSnapshot     = "model.tools.snapshot"
)

// Event families that the worker ignores as a whole. The runtime emits them
// for features LeapMux does not drive: client-executed dynamic tools (LeapMux
// registers none), agent mail, and the `agent.*` family that the runtime drops
// before it reaches a runtime thread (see subagent.go).
var ignoredEventPrefixes = []string{"tool_call.", "agent_mail.", "agent."}

// Turn status words of a turn that has not ended. The final words are in the
// contract, because the browser plugin reads them off the turn end.
const (
	turnStatusQueued     = "queued"
	turnStatusInProgress = "in_progress"
)

// Item status words that only the worker reads: the steer items of a turn
// record in the store (see steer.go). The engine leaves a steer that it never
// committed `queued`, or settles it `canceled`.
const (
	itemStatusQueued    = "queued"
	itemStatusCanceled  = "canceled"
	itemStatusCompleted = "completed"
)

// Goal status words (`thread_goal_updated.goal.status`).
const (
	goalStatusActive        = "active"
	goalStatusPaused        = "paused"
	goalStatusBlocked       = "blocked"
	goalStatusUsageLimited  = "usage_limited"
	goalStatusBudgetLimited = "budget_limited"
	goalStatusComplete      = "complete"
)

// The final status words of the run ledger, GET /v1/agent-runs/{run_id}. The
// ledger also states queued, starting, running, waiting_for_user, model_wait
// and running_tool, and none of them is final. `waiting_for_user` is a child
// parked at a checkpoint until the parent sends it a followup. The browser never
// reads the ledger, so these words are not in the contract.
const (
	agentRunStatusCompleted   = "completed"
	agentRunStatusFailed      = "failed"
	agentRunStatusCancelled   = "cancelled"
	agentRunStatusInterrupted = "interrupted"
)

// Process environment the worker sets for the runtime.
const (
	// envRuntimeToken carries the bearer token. It never reaches argv, where any
	// local user could read it from the process list.
	envRuntimeToken = "CODEWHALE_RUNTIME_TOKEN"
	// envTasksDir places the agent's private task store. The runtime keeps its
	// thread store under `<it>/runtime` and takes an exclusive lock on it.
	envTasksDir = "CODEWHALE_TASKS_DIR"
	// envRuntimeDir places the thread store root directly. It outranks
	// envTasksDir, so the worker pins it to the same place: an inherited value
	// would otherwise move the store away from the directory LeapMux reads.
	envRuntimeDir = "CODEWHALE_RUNTIME_DIR"
	// envHome is Codewhale's state root. LeapMux keeps its stores under it.
	envHome = "CODEWHALE_HOME"
)

// runtimeStoreDir is the directory under a task store that holds the thread
// store.
const runtimeStoreDir = "runtime"

// listeningLinePattern matches the stdout line that states the runtime address.
var listeningLinePattern = regexp.MustCompile(`Runtime API listening on (\S+)`)

// bindFailureText is the part of the runtime's startup error for a port that
// another process took after the worker reserved it.
const bindFailureText = "Failed to bind"

// plumbingStatusPrefixes identify the status items that describe the runtime's
// own loop rather than something the reader acted on. The runtime sends them
// untagged up to 0.10.0: a later release marks them `visibility: internal`,
// which statusItemIsPlumbing reads as well.
//
//   - "Continuing" covers the tool-result and the goal-continuation passes.
//   - "Executing tools sequentially" is the dispatcher's own note.
//   - "Resuming turn with" reports queued subagent completions.
//   - "Loaded deferred tool" is the schema hydration of a first call.
//   - "Steer input accepted" repeats a steer that LeapMux already shows.
//   - "Request cancelled" repeats the interrupted turn end.
//   - "Policy:" (0.9.13) and "Permissions:" (later releases) repeat a posture or
//     mode change, which LeapMux already shows as a settings change.
var plumbingStatusPrefixes = []string{
	"Continuing",
	"Executing tools sequentially",
	"Resuming turn with",
	"Loaded deferred tool",
	"Steer input accepted",
	"Request cancelled",
	"Policy:",
	"Permissions:",
}

// statusItemIsPlumbing reports whether a status item describes the runtime's
// own loop. visibility is the item's `metadata.visibility`, which is empty up
// to 0.10.0.
func statusItemIsPlumbing(summary, visibility string) bool {
	if visibility == "internal" {
		return true
	}
	summary = strings.TrimSpace(summary)
	for _, prefix := range plumbingStatusPrefixes {
		if strings.HasPrefix(summary, prefix) {
			return true
		}
	}
	return false
}
