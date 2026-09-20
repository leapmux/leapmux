/** Chat-specific message helpers (notification thread detection). */

import { NOTIFICATION_TYPE, WORKER_WRITTEN_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'
import { isObject } from '~/lib/jsonPick'

/**
 * The notification types every provider's wrapper test accepts.
 *
 * Two groups. The provider-neutral types an AGENT can also write, listed here; and
 * every type the WORKER writes, which the contract already enumerates. The worker
 * wraps each notification it persists into a `notification_thread` envelope
 * (`wrapNotifContent`), so `parseMessageContent` sets `wrapper` and the
 * worker-written carve-out in `classifyMessage` -- which runs only for an unwrapped
 * row -- never sees one. Without the second group a type the worker alone writes
 * (`agent_status`, `goal_updated`, `goal_cleared`) matched no wrapper test, fell
 * through every provider branch to `unknown`, and drew the raw-frame card instead of
 * its own row.
 *
 * `rate_limit_event` is in NEITHER. Claude Code emits that type and applies its own
 * hidden test to it, so it belongs to Claude's own set -- here it made every other
 * provider's wrapper test accept a type that provider never sends.
 *
 * This set, and NOT the per-message `PLAIN_ROW_TYPES` (~/lib/notificationTypes), is
 * what keeps a thread's OTHER members. A classifier that falls past the wrapper test
 * reads the FIRST member alone and answers `messages: [parent]`, which is right for a
 * one-member thread by accident and drops everything after it. So a type that can sit
 * at any position of a thread belongs here.
 *
 * Every member of `PLAIN_ROW_TYPES` is therefore a member of this set. The per-message
 * set states that a type draws as a row. This set states that a thread holding one keeps
 * its other members. A type in the first and not the second draws its own row and
 * discards every member after it. Nothing fails, so the loss stays invisible.
 * `messageUtils.test.ts` walks the whole `NOTIFICATION_TYPE` vocabulary and fails on the
 * pair. The reverse does not hold: the worker-written types below are in this set alone.
 */
const BASE_NOTIFICATION_TYPES: ReadonlySet<string> = new Set<string>([
  NOTIFICATION_TYPE.SettingsChanged,
  NOTIFICATION_TYPE.ContextCleared,
  NOTIFICATION_TYPE.Interrupted,
  NOTIFICATION_TYPE.PlanUpdated,
  // `plan_execution` is provider-NEUTRAL: the worker writes it for every provider, in
  // LeapMux's own envelope. It travels beside the `context_cleared` that the same plan
  // restart writes just before it, and a thread that holds both answers on
  // `context_cleared` alone. This entry is the one that answers for a thread holding
  // `plan_execution` by itself -- the thread whose `context_cleared` opened a separate
  // row. Without the entry, such a thread reaches `unknown` and draws the raw-frame
  // card.
  NOTIFICATION_TYPE.PlanExecution,
  // `compacting` is LeapMux's own envelope for a compaction that STARTED, and
  // `leapmuxNotificationEntry` draws it for every provider. The worker threads it:
  // `consolidateNotificationThread` holds a case for the type, and two adjacent
  // notifications of one source join one thread whatever their types are. A
  // `compacting` therefore sits beside a `rate_limit` or a provider frame that this set
  // refuses, and this entry is what keeps those members. Without it the wrapper test
  // answers false, and the per-message set then answers for `compacting` alone.
  NOTIFICATION_TYPE.Compacting,
  // `agent_error` is provider-NEUTRAL: `worker/service/agent.go` writes it for every
  // provider, and Pi and the Agent Client Protocol daemons write their own. This entry
  // is what keeps the members of a thread whose one accepted type is `agent_error`, for
  // EVERY provider. Two per-provider sets used to accept it, and each held that one
  // token; this entry replaced both. Codex still hides its own "Codex turn failed" row:
  // that filter reads the members AFTER the wrapper test accepts them.
  NOTIFICATION_TYPE.AgentError,
  ...WORKER_WRITTEN_NOTIFICATION_TYPES,
])

/**
 * Check whether the wrapper envelope represents a notification thread.
 * Accepts an optional set of additional types beyond the base set.
 */
export function isNotificationThreadWrapper(
  wrapper: { messages: unknown[] } | null,
  extraTypes?: Set<string>,
  checkSubtype?: (type: string, subtype: string | undefined) => boolean,
): wrapper is { messages: unknown[] } {
  if (!wrapper || wrapper.messages.length < 1)
    return false
  for (const entry of wrapper.messages) {
    if (!isObject(entry))
      continue
    const t = entry.type as string | undefined
    if (!t)
      continue
    if (BASE_NOTIFICATION_TYPES.has(t))
      return true
    if (extraTypes?.has(t))
      return true
    if (checkSubtype) {
      const st = entry.subtype as string | undefined
      if (checkSubtype(t, st))
        return true
    }
  }
  return false
}

/**
 * A final (non-compacting) `system` status notification -- e.g. the trailing
 * `{type:"system",subtype:"status",status:null}` that ends a compaction. It
 * carries nothing to render: the user-facing "Context compacted (...)" line comes
 * from the separate compact_boundary message, so only the live
 * `status:"compacting"` row is visible. Every provider that surfaces this shape
 * (Claude, Codex, ACP) hides it on BOTH the standalone classifier and the
 * consolidated-thread filter, so a status hidden on its own stays hidden once Hub
 * threads it. Centralized here so "what counts as a final status" can't drift
 * between providers or between the two paths.
 */
export function isFinalCompactingStatus(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'status' && m.status !== 'compacting'
}
