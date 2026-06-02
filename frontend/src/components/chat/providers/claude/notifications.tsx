import type { NotificationThreadEntry } from '../registry'
import { isObject } from '~/lib/jsonPick'
import { formatRateLimitMessage } from '~/lib/rateLimitUtils'

/** Handles rate_limit_event entries within a notification thread (consolidated dividers). */
export function claudeNotificationThreadEntry(
  m: Record<string, unknown>,
): NotificationThreadEntry[] | null {
  const t = m.type as string | undefined
  if (t === 'rate_limit_event') {
    const info = m.rate_limit_info
    if (!isObject(info))
      // A malformed payload (non-object rate_limit_info) still surfaces a
      // generic line rather than vanishing -- classify only routes it here when
      // it isn't an "allowed" status, so it is a real (if rare) notification.
      return [{ kind: 'text', text: 'Rate limit update' }]
    const rlInfo = info as Record<string, unknown>
    if (rlInfo.status !== 'allowed')
      return [{ kind: 'text', text: formatRateLimitMessage(rlInfo) }]
    return []
  }
  return null
}
