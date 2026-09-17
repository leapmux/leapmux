import type { NotificationEntryIR } from '../../../ir/notification'
import { ZCODE_DECISION, ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from './toolCommon'

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
 *
 * A Map rather than a plain object, for the reason `zcode/toolKinds.ts` gives: the
 * key is a WIRE word, and a plain object answers `toString` or `constructor` from
 * `Object.prototype` instead of reporting that it holds no entry. This table holds
 * FUNCTIONS, so that answer was then CALLED -- `toString` drew `[object Undefined]`
 * as the whole sentence, and `__proto__` threw and took the message into the
 * ErrorBoundary.
 */
const ZCODE_DECISION_PHRASE: ReadonlyMap<string, (tool: string) => string> = new Map([
  [ZCODE_DECISION.Allow, (tool: string) => `Allowed ${tool} automatically`],
  [ZCODE_DECISION.Deny, (tool: string) => `Denied ${tool}`],
  [ZCODE_DECISION.Escalate, (tool: string) => `Escalated ${tool} for approval`],
  [ZCODE_DECISION.Modify, (tool: string) => `Ran ${tool} with modified input`],
])

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
      const phrase = ZCODE_DECISION_PHRASE.get(pickString(payload, 'decision'))
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
 * Read one ZCode notification row into the shared notification IR.
 *
 * ZCode's SOLE notification seam, for a standalone row and for one entry of a
 * consolidated wrapper alike -- without it a multi-event thread would render only its
 * first message.
 */
export function zcodeNotificationEntry(msg: Record<string, unknown>): NotificationEntryIR[] {
  const text = describeZCodeNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}
