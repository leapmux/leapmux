/** Chat-specific message helpers (notification thread detection). */

import { isObject } from '~/lib/jsonPick'

/** Default notification thread types recognized by LeapMux (shared across providers). */
const BASE_NOTIFICATION_TYPES = new Set([
  'settings_changed',
  'context_cleared',
  'compacting',
  'interrupted',
  'rate_limit',
  'agent_renamed',
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
