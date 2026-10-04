import type { NotificationEntry } from '../../../model/notification'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_STATUS_TYPE } from '~/generated/contracts/mimo-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { MIMO_PART_FIELD } from '../protocol'
import { mimoEvent } from './toolCommon'

/** The words one `session.error` states: the error's own message, else its name. */
export function mimoErrorText(properties: Record<string, unknown>): { name: string, message: string } {
  const error = pickObject(properties, 'error')
  const data = pickObject(error, 'data')
  return { name: pickString(error, 'name'), message: pickString(data, 'message') }
}

/** True for a compaction part that states the END of its compaction. */
export function mimoCompactionEnded(part: Record<string, unknown>): boolean {
  const projection = part[MIMO_PART_FIELD.Projection]
  return projection !== undefined && projection !== null
}

/**
 * Read one MiMo notification row into the shared notification model.
 *
 * Three of MiMo's own events reach a transcript as notifications:
 *
 *   - `session.status` with a `retry` status: the model call failed, and MiMo waits
 *     before it tries again in the same turn. The thread folds the attempts into the
 *     latest.
 *   - A compaction part, once when the compaction starts and once when it ends with the
 *     summary.
 *   - `session.error` outside a turn: a prompt the server refused before it started a
 *     turn. A failure INSIDE a turn is the turn's divider instead.
 *
 * Returns an empty list for any other row, which the classifier then hides.
 */
export function mimoNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const event = mimoEvent(msg)
  if (!event)
    return []
  switch (event.type) {
    case MIMO_EVENT.SessionStatus: {
      const status = pickObject(event.properties, 'status')
      if (pickString(status, 'type') !== MIMO_STATUS_TYPE.Retry)
        return []
      const attempt = pickNumber(status, 'attempt', undefined)
      const error = pickString(status, 'message') || undefined
      return [{
        kind: 'retry',
        scope: 'api',
        ...(attempt !== undefined ? { attempt } : {}),
        ...(error !== undefined ? { error } : {}),
      }]
    }
    case MIMO_EVENT.MessagePartUpdated: {
      const part = pickObject(event.properties, 'part')
      if (!part || pickString(part, 'type') !== MIMO_PART_TYPE.Compaction)
        return []
      const trigger = part[MIMO_PART_FIELD.Auto] === true ? 'auto' : 'manual'
      return [{ kind: 'compaction', phase: mimoCompactionEnded(part) ? 'end' : 'start', detail: { trigger } }]
    }
    case MIMO_EVENT.SessionError: {
      const { name, message } = mimoErrorText(event.properties)
      const text = [name, message].filter(Boolean).join(': ')
      return text ? [{ kind: 'text', text: `MiMo reported an error: ${text}` }] : []
    }
    default:
      return []
  }
}
