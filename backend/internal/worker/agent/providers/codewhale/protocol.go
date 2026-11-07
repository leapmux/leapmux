package codewhale

import (
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
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
	routeHealth    = "/health"
	routeThreads   = "/v1/threads"
	routeApprovals = "/v1/approvals"
	routeUserInput = "/v1/user-input"
	routeProviders = "/v1/providers"
	routeAgentRuns = "/v1/agent-runs"

	// Thread sub-routes, after /v1/threads/{id}.
	threadRouteResume  = "/resume"
	threadRouteEvents  = "/events"
	threadRouteTurns   = "/turns"
	threadRouteCompact = "/compact"
	threadRouteGoal    = "/goal"
	// The current context report and individual shell jobs belong to the thread.
	threadRouteContext = "/context"
	threadRouteJobs    = "/jobs"

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
	eventOperationActivityStarted   = "operation.activity_started"
	eventOperationActivityCompleted = "operation.activity_completed"
	eventOperationHeartbeat         = "operation.heartbeat"
	eventToolExecutionStarted       = "tool.execution_started"
	eventThreadStarted              = "thread.started"
	eventThreadUpdated              = "thread.updated"
	eventThreadPermissionsChanged   = "thread.permissions_changed"
	eventThreadForked               = "thread.forked"
	eventTurnStarted                = "turn.started"
	eventTurnLifecycle              = "turn.lifecycle"
	eventTurnUsage                  = "turn.usage"
	eventTurnProviderRequest        = "turn.provider_request"
	eventTurnSteered                = "turn.steered"
	eventTurnInterruptRequested     = "turn.interrupt_requested"
	eventItemDelta                  = "item.delta"
	eventApprovalDecided            = "approval.decided"
	eventUserInputAnswered          = "user_input.answered"
	eventGoalUpdated                = "thread_goal_updated"
	eventGoalCleared                = "thread_goal_cleared"
	eventModelToolsSnapshot         = "model.tools.snapshot"
	eventTurnArtifacts              = "turn.artifacts"
	eventTurnWorkspaceSnapshot      = "turn.workspace_snapshot"
	eventApprovalGrantAdded         = "approval.grant_added"
	eventApprovalGrantRevoked       = "approval.grant_revoked"
)

// Event families that the worker ignores as a whole. The runtime emits them
// for features LeapMux does not drive: client-executed dynamic tools (LeapMux
// registers none), agent mail, and the `agent.*` family that the runtime drops
// before it reaches a runtime thread (see subagent.go).
var ignoredEventPrefixes = []string{"tool_call.", "agent_mail.", "agent."}

// itemKindUserMessage identifies a native prompt echo and stored steer history. Only the worker reads it.
const itemKindUserMessage = "user_message"

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

// Status words of the agent tool's result. An interrupted child can resume
// from a checkpoint, so only completed, failed, and cancelled close its
// registry row. The browser never reads these native words.
const (
	agentRunStatusRunning     = "running"
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

// statusItemIsPlumbing hides explicit internal notes and the unmarked queued-steer diagnostic.
// The runtime leaves queued-steer diagnostics without visibility metadata.
func statusItemIsPlumbing(summary string, visibility string) bool {
	return visibility == "internal" || strings.HasPrefix(summary, "Steer input queued: ")
}

// These memberships come from the same contract subsets that the browser reads.
var (
	codewhaleNotificationEvents    = notificationMemberSet(contracts.CodewhaleNotificationEventMembers)
	codewhaleNotificationItemKinds = notificationMemberSet(contracts.CodewhaleNotificationItemKindMembers)
)

func notificationMemberSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
