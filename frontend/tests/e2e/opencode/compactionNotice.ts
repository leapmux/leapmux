import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { OPENCODE_EVENT } from '../../../src/generated/contracts/opencode-protocol'
import { NOTIFICATION_THREAD_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'

/** Read completed native events from the exact session's stored notification threads. */
export function openCodeCompactionEvents(snapshot: NativeMessageSnapshot): Record<string, unknown>[] {
  if (!snapshot.agentId.trim() || !snapshot.agentSessionId.trim())
    throw new Error('The compaction event read requires an agent and a native session identity.')
  const events: Record<string, unknown>[] = []
  for (const message of snapshot.messages) {
    if (message.source !== MessageSource.AGENT || message.agentSessionId !== snapshot.agentSessionId)
      continue
    const frame = nativeMessageBody(message)
    if (!isObject(frame) || frame.type !== NOTIFICATION_THREAD_TYPE || !Array.isArray(frame.messages))
      continue
    for (const event of frame.messages) {
      if (isObject(event) && event.type === OPENCODE_EVENT.SessionCompacted
        && pickObject(event, 'properties')?.sessionID === snapshot.agentSessionId) {
        events.push(event)
      }
    }
  }
  return events
}
