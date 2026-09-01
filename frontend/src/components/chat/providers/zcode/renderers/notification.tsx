import type { NotificationThreadEntry } from '../../registry'
import { ZCODE_DECISION, ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from '../extractors/toolCommon'

/**
 * The sentence for one resolved permission, per decision.
 *
 * Only a decision the app-server made BY ITSELF reaches a row -- one the user
 * answered is already recorded as its own answer row.
 *
 * This table lists every decision in the app-server's enumeration, and a decision that is
 * absent or outside it yields null: reporting an unread decision as a denial would
 * state the opposite of an `allow` and would turn an escalation into a refusal that
 * never happened.
 */
const ZCODE_DECISION_PHRASE: Record<string, (tool: string) => string> = {
  [ZCODE_DECISION.Allow]: tool => `Allowed ${tool} automatically`,
  [ZCODE_DECISION.Deny]: tool => `Denied ${tool}`,
  [ZCODE_DECISION.Escalate]: tool => `Escalated ${tool} for approval`,
  [ZCODE_DECISION.Modify]: tool => `Ran ${tool} with modified input`,
}

/**
 * A human-readable line for one ZCode notification row.
 *
 * Returns null for a shape ZCode does not own, so the shared provider-neutral
 * notification switch (settings_changed, interrupted, ...) can try it instead -- and
 * for a row of ZCode's own whose decision it cannot read, which the plugin then
 * hides rather than surfacing as an empty notification.
 */
export function describeZCodeNotification(parsed: unknown): string | null {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope)
    return null
  const payload = envelope.payload

  switch (envelope.type) {
    case ZCODE_EVENT.PermissionResolved: {
      const phrase = ZCODE_DECISION_PHRASE[pickString(payload, 'decision')]
      if (!phrase)
        return null
      const head = phrase(pickString(payload, 'toolName') || 'a tool')
      const reason = pickString(payload, 'reason')
      return reason ? `${head} — ${reason}` : head
    }

    case ZCODE_EVENT.TurnSteerQueued: {
      const preview = pickString(payload, 'inputPreview')
      return preview ? `Queued for the running turn: ${preview}` : 'Queued for the running turn'
    }

    case ZCODE_EVENT.TurnSteerDrained:
      return 'Queued input delivered to the agent'

    case ZCODE_EVENT.SessionClosed: {
      const reason = pickString(payload, 'reason')
      return reason ? `Session closed — ${reason}` : 'Session closed'
    }

    default:
      return null
  }
}

/**
 * Convert one ZCode notification row into thread entries for the shared
 * `renderNotificationThread`.
 *
 * This is ZCode's SOLE notification render seam, for a standalone row and for one
 * entry of a consolidated `notification_thread` wrapper alike -- without it a
 * multi-event thread would render only its first message.
 */
export function zcodeNotificationThreadEntry(msg: Record<string, unknown>): NotificationThreadEntry[] | null {
  const text = describeZCodeNotification(msg)
  if (text === null)
    return null
  return [{ kind: 'text', text }]
}
