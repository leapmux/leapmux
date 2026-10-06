import type { AgentEvent } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import type { AgentWatchServer } from './agentEventWatch'
import { SESSION_INFO_KEY } from '../../../src/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { watchAgentEvents } from './agentEventWatch'

/**
 * The context-usage map of one live session-info update, or undefined for any
 * other event. A replayed event is history, not a live update, so it is skipped.
 */
export function contextUsageReading(event: AgentEvent): Record<string, unknown> | undefined {
  if (event.replay || event.event.case !== 'agentMessage')
    return undefined
  const message = event.event.value
  if (message.seq !== -1n)
    return undefined
  let raw: string | null
  try {
    raw = decompressContentToString(message.content, message.contentCompression)
  }
  catch (error) {
    throw new Error('The Worker sent invalid compressed usage content.', { cause: error })
  }
  if (!raw)
    return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  }
  catch {
    return undefined
  }
  if (!isObject(parsed) || pickString(parsed, 'type') !== NOTIFICATION_TYPE.AgentSessionInfo)
    return undefined
  return pickObject(pickObject(parsed, 'info'), SESSION_INFO_KEY.ContextUsage) ?? undefined
}

/** Watch the native usage map that the Worker publishes for one agent. */
export async function watchAgentContextUsage(server: AgentWatchServer, agentId: string): Promise<{ readings: () => readonly Record<string, unknown>[], cancel: () => void }> {
  const watch = await watchAgentEvents(server, agentId, { label: 'usage watch', select: contextUsageReading })
  return { readings: watch.items, cancel: watch.cancel }
}
