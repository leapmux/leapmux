import type { NotificationEntry } from '../../../model/notification'
import { OPENCODE_NATIVE_COMPACTION_STATUS, OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_MESSAGE_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'

/** Read the structured native error without interpreting response-body bytes. */
export function openCodeErrorText(value: unknown): { type: string, message: string } {
  const error = isObject(value)
    ? value
    : undefined
  return {
    type: pickString(error, 'type'),
    message: pickString(error, 'message'),
  }
}

/** Identify native compaction starts for the history filter. */
export function openCodeCompactionStart(message: Record<string, unknown>): boolean {
  return message.type === OPENCODE_NATIVE_EVENT.CompactionStarted
    || message.type === OPENCODE_NATIVE_EVENT.CompactionDelta
    || (message.type === OPENCODE_NATIVE_MESSAGE_TYPE.Compaction && message.status === OPENCODE_NATIVE_COMPACTION_STATUS.Running)
}

/** Read live and recovered compaction records and native retry notices. */
export function openCodeNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const type = pickString(message, 'type')
  const data = type === OPENCODE_NATIVE_MESSAGE_TYPE.Compaction
    ? message
    : pickObject(message, 'data')
  if (!data)
    return []
  if (type === OPENCODE_NATIVE_EVENT.RetryScheduled) {
    const at = pickNumber(data, 'at', undefined)
    const created = pickNumber(message, 'created', undefined)
    const attempt = pickNumber(data, 'attempt', undefined)
    const error = openCodeErrorText(pickObject(data, 'error')).message
    return [
      retryEntry('api', {
        ...(attempt !== undefined
          ? { attempt }
          : {}),
        ...(at !== undefined && created !== undefined
          ? {
              delayMs: Math.max(0, at - created),
            }
          : {}),
        ...(error
          ? { error }
          : {}),
      }),
    ]
  }
  const trigger = pickString(data, 'reason')
  const detail = trigger
    ? { trigger }
    : undefined
  if (openCodeCompactionStart(message)) {
    return [
      {
        kind: 'compaction',
        phase: 'start',
        ...(detail
          ? { detail }
          : {}),
      },
    ]
  }
  const status = pickString(data, 'status')
  if (type === OPENCODE_NATIVE_EVENT.CompactionEnded
    || (type === OPENCODE_NATIVE_MESSAGE_TYPE.Compaction && status === OPENCODE_NATIVE_COMPACTION_STATUS.Completed)) {
    return [
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        ...(detail
          ? { detail }
          : {}),
      },
    ]
  }
  if (type === OPENCODE_NATIVE_EVENT.CompactionFailed
    || (type === OPENCODE_NATIVE_MESSAGE_TYPE.Compaction && status === OPENCODE_NATIVE_COMPACTION_STATUS.Failed)) {
    const error = openCodeErrorText(pickObject(data, 'error')).message
    return [
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'failed',
        ...(detail
          ? { detail }
          : {}),
        ...(error
          ? { error }
          : {}),
      },
    ]
  }
  return []
}
