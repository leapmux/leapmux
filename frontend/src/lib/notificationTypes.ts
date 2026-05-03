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
} as const

export type NotificationType = typeof NOTIFICATION_TYPE[keyof typeof NOTIFICATION_TYPE]
