import type { CompactionDetails, NotificationEntry } from '../../model/notification'
import type { ACPNativeNotifications } from '../acp/notification'
import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'
import { QWEN_COMPRESSION_PHASE, QWEN_MESSAGE_SOURCE, QWEN_META } from '~/generated/contracts/qwen-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { tokenCount } from '../../model/notification'

const BACKGROUND_SOURCES: ReadonlySet<string> = new Set([
  QWEN_MESSAGE_SOURCE.BackgroundTaskCompleted,
  QWEN_MESSAGE_SOURCE.BackgroundNotification,
  QWEN_MESSAGE_SOURCE.BackgroundResponse,
])

function qwenMetadata(message: Record<string, unknown>): Record<string, unknown> | null {
  return message.sessionUpdate === ACP_UPDATE.AgentMessageChunk
    ? pickObject(message, '_meta')
    : null
}

export function qwenCompactionBoundary(message: Record<string, unknown>): CompactionDetails | null {
  const compression = pickObject(qwenMetadata(message), QWEN_META.ContextCompression)
  if (pickString(compression, 'phase') !== QWEN_COMPRESSION_PHASE.Done)
    return null
  const pre = tokenCount(compression?.originalTokenCount)
  const post = tokenCount(compression?.newTokenCount)
  return {
    ...(pre !== undefined
      ? { pre }
      : {}),
    ...(post !== undefined
      ? { post }
      : {}),
  }
}

/** Identify a stored start for the shared safety net without assigning it a visible start entry. */
export function qwenCompactionStart(message: Record<string, unknown>): boolean {
  return pickString(pickObject(qwenMetadata(message), QWEN_META.ContextCompression), 'phase') === QWEN_COMPRESSION_PHASE.Progress
}

export function qwenNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const meta = qwenMetadata(message)
  if (!meta)
    return []
  if (QWEN_META.ContextCompression in meta) {
    const detail = qwenCompactionBoundary(message)
    if (!detail)
      return []
    const entries: NotificationEntry[] = [
      { kind: 'compaction', phase: 'end', detail },
    ]
    const warning = pickString(pickObject(meta, QWEN_META.ContextCompression), 'warning').trim()
    if (warning)
      entries.push({ kind: 'text', text: warning })
    return entries
  }
  const hook = pickObject(meta, QWEN_META.StopHookLoop)
  if (hook) {
    const detail = Array.isArray(hook.reasons)
      ? hook.reasons.filter((reason): reason is string => typeof reason === 'string').map(reason => reason.trim()).filter(Boolean).join('; ')
      : ''
    return [
      {
        kind: 'hook',
        outcome: 'continued',
        event: 'Stop',
        ...(detail
          ? { detail }
          : {}),
      },
    ]
  }
  if (meta[QWEN_META.DiscreteMessage] !== true || BACKGROUND_SOURCES.has(pickString(meta, QWEN_META.Source)))
    return []
  const text = pickString(pickObject(message, 'content'), 'text')
  return text.trim()
    ? [
        { kind: 'text', text },
      ]
    : []
}

export const qwenNativeNotifications: ACPNativeNotifications = {
  isMember: message => qwenNotificationEntry(message).length > 0,
  entries: qwenNotificationEntry,
  compactionBoundary: qwenCompactionBoundary,
  compactionStart: qwenCompactionStart,
}
