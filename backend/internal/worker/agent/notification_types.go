package agent

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
	NotificationTypeAgentError = "agent_error"

	// NotificationTypeSettingsChanged is emitted when the user updates the
	// agent's model / effort / permission mode / extra settings. Carries
	// a `changes` map of {key: {old, new}} entries.
	NotificationTypeSettingsChanged = "settings_changed"

	// NotificationTypeContextCleared is emitted when the agent's context is
	// cleared in place (e.g. /clear) or via a fresh restart. Marks a turn
	// boundary for the working-state heuristic.
	NotificationTypeContextCleared = "context_cleared"

	// NotificationTypeInterrupted is emitted when the user interrupts an
	// in-flight turn. Marks a real turn end on the frontend.
	NotificationTypeInterrupted = "interrupted"

	// NotificationTypePlanExecution is emitted when the worker initiates
	// plan-mode execution. Carries plan metadata (file path, title).
	NotificationTypePlanExecution = "plan_execution"

	// NotificationTypePlanUpdated is emitted when the active plan file
	// changes — either a new file path was chosen or the title rotated.
	NotificationTypePlanUpdated = "plan_updated"

	// NotificationTypeCompacting is the wire shape for ACP/Codex
	// compaction-progress notifications surfaced as system events.
	NotificationTypeCompacting = "compacting"

	// NotificationTypeAgentSessionInfo carries an ephemeral session-info
	// payload (cost, context usage, rate limits) outside the message
	// stream. Frontends route it through agentSessionStore, not the chat
	// renderer.
	NotificationTypeAgentSessionInfo = "agent_session_info"

	// NotificationTypeRateLimit / NotificationTypeRateLimitEvent are the
	// two wire shapes Claude / Codex use for rate-limit metadata; both
	// route into the rate-limit popover.
	NotificationTypeRateLimit      = "rate_limit"
	NotificationTypeRateLimitEvent = "rate_limit_event"
)
