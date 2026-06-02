/** Chat-specific message helpers (notification thread detection). */

import { isObject } from '~/lib/jsonPick'

/** Default notification thread types recognized by LeapMux (shared across providers). */
const BASE_NOTIFICATION_TYPES = new Set([
  'settings_changed',
  'context_cleared',
  'interrupted',
  'rate_limit_event',
  'plan_updated',
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
 * A terminal (non-compacting) `system` status notification -- e.g. the trailing
 * `{type:"system",subtype:"status",status:null}` that ends a compaction. It
 * carries nothing to render: the user-facing "Context compacted (...)" line comes
 * from the separate compact_boundary message, so only the live
 * `status:"compacting"` row is visible. Every provider that surfaces this shape
 * (Claude, Codex, ACP) hides it on BOTH the standalone classifier and the
 * consolidated-thread filter, so a status hidden on its own stays hidden once Hub
 * threads it. Centralized here so "what counts as a terminal status" can't drift
 * between providers or between the two paths.
 */
export function isTerminalCompactingStatus(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'status' && m.status !== 'compacting'
}
