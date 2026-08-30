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
import { WORKER_AUTHORED_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'

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
const WORKER_AUTHORED: ReadonlySet<string> = new Set(WORKER_AUTHORED_NOTIFICATION_TYPES)

/**
 * True when `parentObject` is a worker-authored notification envelope. The
 * worker persists these as standalone rows, so the caller checks that the row
 * carries no notification-thread wrapper before it asks.
 */
export function isWorkerAuthoredNotification(parentObject: unknown): boolean {
  if (typeof parentObject !== 'object' || parentObject === null)
    return false
  const type = (parentObject as { type?: unknown }).type
  return typeof type === 'string' && WORKER_AUTHORED.has(type)
}
