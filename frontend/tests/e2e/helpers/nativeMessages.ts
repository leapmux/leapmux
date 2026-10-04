import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'
import { nativeAgentById } from './nativeScenario'

export interface NativeMessageSnapshot {
  agentId: string
  /** Empty only for a virtual child with verified, stable owner links. */
  agentSessionId: string
  messages: AgentChatMessage[]
}

/** Read every Worker message while the agent identity remains unchanged. */
export async function readNativeMessageSnapshot(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
): Promise<NativeMessageSnapshot> {
  if (agentId.trim() === '')
    throw new Error('The native message read requires a nonempty agent ID.')
  const before = await nativeAgentById(context, agentId)
  if (!before || before.id !== agentId || before.status !== AgentStatus.ACTIVE)
    throw new Error('The native message read requires a started agent.')
  // The Worker creates virtual children without a native session of their own.
  const linkedVirtualChild = before.agentSessionId === ''
    && before.parentAgentId.trim() !== ''
    && (before.spawnSpanId.trim() !== '' || before.providerChildKey.trim() !== '')
    && before.rootAgentId.trim() !== ''
  if (before.agentSessionId.trim() === '' && !linkedVirtualChild)
    throw new Error('The native message read requires a started agent with a native session or linked virtual child.')
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const messages: AgentChatMessage[] = []
  const seen = new Set<string>()
  let cursor: bigint | undefined
  for (;;) {
    const response = await channel.callWorker(server.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, {
      agentId,
      anchor: cursor === undefined ? MessagePageAnchor.OLDEST : MessagePageAnchor.AFTER,
      ...(cursor === undefined ? {} : { cursorSeq: cursor }),
      limit: MESSAGE_PAGE_LIMIT,
    })
    if (response.messages.length === 0 && response.hasMore)
      throw new Error('The Worker message page is empty but claims another page.')
    for (const message of response.messages) {
      if (message.id.trim() === '' || seen.has(message.id))
        throw new Error('The Worker message page contains an absent or duplicate message ID.')
      if (message.seq < 0n || (cursor !== undefined && message.seq <= cursor))
        throw new Error('The Worker message cursor did not advance.')
      seen.add(message.id)
      messages.push(message)
      cursor = message.seq
    }
    if (!response.hasMore)
      break
  }
  const after = await nativeAgentById(context, agentId)
  if (!after || after.status !== AgentStatus.ACTIVE || after.id !== before.id || after.agentSessionId !== before.agentSessionId
    || after.parentAgentId !== before.parentAgentId || after.spawnSpanId !== before.spawnSpanId || after.rootAgentId !== before.rootAgentId || after.providerChildKey !== before.providerChildKey) {
    throw new Error('The native session changed or its agent ownership links changed during the Worker message read.')
  }
  return { agentId, agentSessionId: before.agentSessionId, messages }
}

/** Decode common Worker bytes. The provider reader interprets the returned JSON value. */
export function nativeMessageBody(message: AgentChatMessage): unknown {
  const text = decompressContentToString(message.content, message.contentCompression)
  if (text === null)
    throw new Error('The native message uses an unsupported content compression.')
  try {
    return JSON.parse(text)
  }
  catch (cause) {
    throw new Error('The native message contains invalid JSON.', { cause })
  }
}

/** Decode a present Worker supplement without interpreting its provider section. */
export function nativeMessageSupplement(message: AgentChatMessage): unknown {
  if (message.supplementalContent.byteLength === 0)
    return undefined
  const text = decompressContentToString(message.supplementalContent, message.supplementalContentCompression)
  if (text === null)
    throw new Error('The native message supplement uses an unsupported content compression.')
  if (text === '')
    return undefined
  try {
    return JSON.parse(text)
  }
  catch (cause) {
    throw new Error('The native message supplement contains invalid JSON.', { cause })
  }
}

export interface NativeToolOutputRecordOptions {
  callId: string
  spanId: string | ((frame: Record<string, unknown>) => string)
  accepts: (frame: Record<string, unknown>) => boolean
}

export interface NativeToolOutputRecord {
  message: AgentChatMessage
  frame: Record<string, unknown>
  supplement: unknown
}

/** Select one original Worker record. The provider interprets its native fields. */
export function readNativeToolOutputRecord(snapshot: NativeMessageSnapshot, options: NativeToolOutputRecordOptions): NativeToolOutputRecord {
  if (!snapshot.agentId.trim() || !snapshot.agentSessionId.trim() || !options.callId.trim()
    || (typeof options.spanId === 'string' && !options.spanId.trim())) {
    throw new Error('The native output record requires an agent, session, call, and span owner.')
  }
  const records: NativeToolOutputRecord[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId
      || (typeof options.spanId === 'string' && message.spanId !== options.spanId)) {
      continue
    }
    const frame = nativeMessageBody(message)
    if (!isObject(frame) || !options.accepts(frame))
      continue
    const spanId = typeof options.spanId === 'string' ? options.spanId : options.spanId(frame)
    if (!spanId.trim())
      throw new Error('The native output record callback supplied an empty span owner.')
    if (message.spanId !== spanId)
      continue
    records.push({ message, frame, supplement: nativeMessageSupplement(message) })
  }
  if (records.length !== 1 || !records[0])
    throw new Error('The native output requires exactly one accepted record in its Worker session and span.')
  return records[0]
}
