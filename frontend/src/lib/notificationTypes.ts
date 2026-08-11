/**
 * LeapMux notification-type vocabulary. Mirrors the backend constants in
 * `backend/internal/worker/agent/notification_types.go`. The platform
 * persists each of these as the inner `type` field on a notification
 * envelope (LEAPMUX source for worker-synthesized events, AGENT source
 * for agent-emitted metadata that flows through the same renderer).
 *
 * Importing the constant from this module instead of inlining the wire
 * string turns rename mistakes into compile errors and gives the
 * dispatch sites a single source of truth.
 */
export const NOTIFICATION_TYPE = {
  AgentError: 'agent_error',
  SettingsChanged: 'settings_changed',
  ContextCleared: 'context_cleared',
  Interrupted: 'interrupted',
  PlanExecution: 'plan_execution',
  PlanUpdated: 'plan_updated',
  Compacting: 'compacting',
  AgentSessionInfo: 'agent_session_info',
  RateLimit: 'rate_limit',
  RateLimitEvent: 'rate_limit_event',
  SubagentEnded: 'subagent_ended',
} as const

export type NotificationType = typeof NOTIFICATION_TYPE[keyof typeof NOTIFICATION_TYPE]

/**
 * The types the WORKER synthesizes, as opposed to the ones an agent emits.
 *
 * A worker-authored notification is provider-neutral by construction: no agent
 * produces it, so no provider plugin can recognize it from its own wire format.
 * `classifyMessage` therefore classifies these once, before it dispatches to a
 * plugin. Adding a type here and to `NOTIFICATION_TYPE` is the whole
 * registration; a per-provider table cannot be left half-updated.
 *
 * Add a type here ONLY when the worker is its sole writer. An agent-emitted type
 * stays out, because a plugin may legitimately suppress or reshape one.
 */
const WORKER_AUTHORED_NOTIFICATION_TYPES: ReadonlySet<string> = new Set([
  NOTIFICATION_TYPE.SubagentEnded,
])

/**
 * True when `parentObject` is a worker-authored notification envelope. The
 * worker persists these as standalone rows, so the caller checks that the row
 * carries no notification-thread wrapper before it asks.
 */
export function isWorkerAuthoredNotification(parentObject: unknown): boolean {
  if (typeof parentObject !== 'object' || parentObject === null)
    return false
  const type = (parentObject as { type?: unknown }).type
  return typeof type === 'string' && WORKER_AUTHORED_NOTIFICATION_TYPES.has(type)
}
