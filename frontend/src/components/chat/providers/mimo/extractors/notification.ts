import type { NotificationEntry, RetryFacts } from '../../../model/notification'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_STATUS_TYPE } from '~/generated/contracts/mimo-protocol'
import { assignDefined, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { MIMO_PART_FIELD } from '../protocol'
import { mimoEvent } from './toolCommon'

/** The words one `session.error` states: the error's own message, else its name. */
export function mimoErrorText(properties: Record<string, unknown>): { name: string, message: string } {
  const error = pickObject(properties, 'error')
  const data = pickObject(error, 'data')
  return {
    name: pickString(error, 'name'),
    message: pickString(data, 'message'),
  }
}

/** True for a compaction part that states the END of its compaction. */
export function mimoCompactionEnded(part: Record<string, unknown>): boolean {
  const projection = part[MIMO_PART_FIELD.Projection]
  return projection !== undefined && projection !== null
}

/** Identify an older stored start for the shared compaction safety net. */
export function mimoCompactionStart(msg: Record<string, unknown>): boolean {
  const event = mimoEvent(msg)
  const part = event && pickObject(event.properties, 'part')
  return event?.type === MIMO_EVENT.MessagePartUpdated
    && !!part && pickString(part, 'type') === MIMO_PART_TYPE.Compaction && !mimoCompactionEnded(part)
}

/** Read native retries, compaction ends, and errors into the neutral notification model. */
export function mimoNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const event = mimoEvent(msg)
  if (!event)
    return []
  switch (event.type) {
    case MIMO_EVENT.SessionRetryAttempt: {
      const facts: RetryFacts = {
        error: pickString(event.properties, 'reason'),
      }
      assignDefined(facts, 'attempt', pickNumber(event.properties, 'attempt', undefined))
      assignDefined(facts, 'maxAttempts', pickNumber(event.properties, 'maxAttempts', undefined))
      assignDefined(facts, 'delayMs', pickNumber(event.properties, 'nextDelayMs', undefined))
      return [
        retryEntry('api', facts),
      ]
    }
    case MIMO_EVENT.SessionStatus: {
      const status = pickObject(event.properties, 'status')
      if (pickString(status, 'type') === MIMO_STATUS_TYPE.Notice) {
        const text = pickString(status, 'message').trim()
        return text
          ? [
              { kind: 'status', text },
            ]
          : []
      }
      if (pickString(status, 'type') !== MIMO_STATUS_TYPE.Retry)
        return []
      const attempt = pickNumber(status, 'attempt', undefined)
      const error = pickString(status, 'message') || undefined
      return [
        retryEntry('api', {
          ...(attempt !== undefined
            ? { attempt }
            : {}),
          ...(error !== undefined
            ? { error }
            : {}),
        }),
      ]
    }
    case MIMO_EVENT.MessagePartUpdated: {
      const part = pickObject(event.properties, 'part')
      if (!part || pickString(part, 'type') !== MIMO_PART_TYPE.Compaction || !mimoCompactionEnded(part))
        return []
      const trigger = part[MIMO_PART_FIELD.Auto] === true
        ? 'auto'
        : 'manual'
      return [
        {
          kind: 'compaction',
          phase: 'end',
          detail: { trigger },
        },
      ]
    }
    case MIMO_EVENT.SessionError: {
      const { name, message } = mimoErrorText(event.properties)
      const text = [name, message].filter(Boolean).join(': ')
      return text
        ? [
            { kind: 'text', text: `MiMo reported an error: ${text}` },
          ]
        : []
    }
    default:
      return []
  }
}
