/**
 * LeapMux notification-type classification. The NOTIFICATION_TYPE vocabulary
 * itself is generated from contracts/worker-vocab.json into
 * ~/generated/contracts/worker-vocab (the Go twin is
 * backend/internal/worker/agent/notification_types.go); import it there. The
 * platform persists each token as the inner `type` field on a notification
 * envelope (LEAPMUX source for worker-synthesized events, AGENT source
 * for agent-emitted metadata that flows through the same renderer).
 *
 * This module keeps the classification logic that has no generated twin.
 */
import { NOTIFICATION_TYPE, WORKER_WRITTEN_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'

/**
 * The types the WORKER synthesizes, as opposed to the ones an agent emits.
 *
 * A worker-written notification is provider-neutral by construction: no agent
 * produces it, so no provider plugin can recognize it from its own wire format.
 * `classifyMessage` therefore classifies these once, before it dispatches to a
 * plugin. Adding a type here and to `NOTIFICATION_TYPE` is the whole
 * registration; a per-provider table cannot be left half-updated.
 *
 * Add a type here ONLY when the worker is its sole writer. An agent-emitted type
 * stays out, because a plugin may legitimately suppress or reshape one.
 */
const WORKER_WRITTEN: ReadonlySet<string> = new Set(WORKER_WRITTEN_NOTIFICATION_TYPES)

/**
 * True when `parentObject` is a worker-written notification envelope. The
 * worker persists these as standalone rows, so the caller checks that the row
 * carries no notification-thread wrapper before it asks.
 */
export function isWorkerWrittenNotification(parentObject: unknown): boolean {
  if (typeof parentObject !== 'object' || parentObject === null)
    return false
  const type = (parentObject as { type?: unknown }).type
  return typeof type === 'string' && WORKER_WRITTEN.has(type)
}

/**
 * The notification types a provider renders as an ordinary row, unchanged.
 *
 * Each arrives in LeapMux's own envelope -- a `type` and no provider frame -- so a
 * plugin has nothing of its own to read in one, and every plugin that meets one
 * reaches the same answer. The set is NOT worker-written: an agent writes several of
 * these (Claude Code emits its own `interrupted`), which is why `classifyMessage`
 * cannot take them ahead of the plugin the way it takes the worker-written ones.
 *
 * `rate_limit_event` stays out. Claude Code applies its own hidden test to that type,
 * so it is not one answer for every provider.
 *
 * This set answers for ONE message. It is not the set that keeps a thread alive: the
 * wrapper test (`BASE_NOTIFICATION_TYPES` in ~/components/chat/messageUtils) is, and a
 * type that can sit at any position of a thread needs an entry there too.
 *
 * Every type here is therefore a type that set accepts. A classifier reaches this one
 * after the wrapper test found nothing, and it answers for the FIRST member alone -- so
 * a type here and not there draws its own row and discards every member after it.
 * `messageUtils.test.ts` walks the whole vocabulary and fails on the pair.
 */
const PLAIN_ROW_TYPES: ReadonlySet<string> = new Set([
  NOTIFICATION_TYPE.SettingsChanged,
  NOTIFICATION_TYPE.ContextCleared,
  NOTIFICATION_TYPE.Interrupted,
  NOTIFICATION_TYPE.AgentError,
  NOTIFICATION_TYPE.PlanUpdated,
  NOTIFICATION_TYPE.Compacting,
  // The worker writes `plan_execution` in LeapMux's own envelope for EVERY provider,
  // and `leapmuxNotificationEntry` draws the one row for it, so every plugin that
  // meets one reaches the same answer. This entry answers for an UNWRAPPED row of the
  // type. The wrapper test answers for a threaded one.
  NOTIFICATION_TYPE.PlanExecution,
])

/**
 * True for a notification type that renders as a plain row in every provider.
 *
 * A plugin calls this where its own vocabulary found no match, so a LeapMux row
 * reaches the notification renderer rather than the raw-JSON fallback.
 */
export function isPlainNotificationType(type: string | undefined): boolean {
  return type !== undefined && PLAIN_ROW_TYPES.has(type)
}
