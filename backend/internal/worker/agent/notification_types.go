package agent

import "github.com/leapmux/leapmux/generated/contracts"

// LeapMux notification-type vocabulary. The platform persists each of
// these as the inner `type` field on a notification envelope (LEAPMUX
// source for worker-synthesized events; AGENT source for agent-emitted
// metadata that flows through the same renderer). Centralizing the
// strings turns rename mistakes into compile errors and gives the
// dispatch switches a single source of truth.
const (
	// NotificationTypeAgentError is a worker-emitted agent failure (startup
	// crash, restart failure, settings-apply failure). Carries an `error`
	// string with the user-facing reason.
	NotificationTypeAgentError = contracts.NotificationTypeAgentError

	// NotificationTypeSettingsChanged is emitted when the user updates the
	// agent's model / effort / permission mode / options. Carries
	// a `changes` map of {key: {old, new}} entries.
	NotificationTypeSettingsChanged = contracts.NotificationTypeSettingsChanged

	// NotificationTypeContextCleared is emitted when the agent's context is
	// cleared in place (e.g. /clear) or via a fresh restart. Marks a turn
	// boundary for the working-state heuristic.
	NotificationTypeContextCleared = contracts.NotificationTypeContextCleared

	// NotificationTypeInterrupted is emitted when the user interrupts an
	// in-flight turn. Marks a real turn end on the frontend.
	NotificationTypeInterrupted = contracts.NotificationTypeInterrupted

	// NotificationTypePlanExecution is emitted when the worker initiates
	// plan-mode execution. Carries plan metadata (file path, title).
	NotificationTypePlanExecution = contracts.NotificationTypePlanExecution

	// NotificationTypePlanUpdated is emitted when the active plan file
	// changes — either a new file path was chosen or the title rotated.
	NotificationTypePlanUpdated = contracts.NotificationTypePlanUpdated

	// NotificationTypeCompacting is the wire shape for ACP/Codex
	// compaction-progress notifications surfaced as system events.
	NotificationTypeCompacting = contracts.NotificationTypeCompacting

	// NotificationTypeAgentSessionInfo carries an ephemeral session-info
	// payload (cost, context usage, rate limits) outside the message
	// stream. Frontends route it through agentSessionStore, not the chat
	// renderer.
	NotificationTypeAgentSessionInfo = contracts.NotificationTypeAgentSessionInfo

	// NotificationTypeRateLimit / NotificationTypeRateLimitEvent are the
	// two wire shapes Claude / Codex use for rate-limit metadata; both
	// route into the rate-limit popover.
	NotificationTypeRateLimit      = contracts.NotificationTypeRateLimit
	NotificationTypeRateLimitEvent = contracts.NotificationTypeRateLimitEvent

	// NotificationTypeSubagentEnded closes a subagent RUN. The worker writes one
	// into a child transcript each time that subagent's background-task row
	// reaches a final status, so the subagent tab shows WHERE it stopped and WHY
	// instead of a thinking indicator that never resolves. Carries a `status`
	// field holding the registry's final wire status (completed / failed /
	// stopped / interrupted). Provider-neutral: the registry close is the one
	// moment every provider agrees a subagent is over, including the ones whose
	// child transcript simply stops.
	//
	// One per run, NOT one per transcript, and nothing follows it only until
	// something does. Claude restarts a finished subagent when the parent
	// messages it, and the restarted run ends the same way -- so a transcript
	// holds as many of these as the subagent had runs, each with more messages
	// below it.
	NotificationTypeSubagentEnded = contracts.NotificationTypeSubagentEnded
)
