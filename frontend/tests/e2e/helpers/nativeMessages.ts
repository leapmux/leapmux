import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from './api'
import { jsonStringValues } from './jsonStringValues'
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
  const messages = await readAllAgentMessages(context, agentId)
  const after = await nativeAgentById(context, agentId)
  if (!after || after.status !== AgentStatus.ACTIVE || !sameAgentOwnership(before, after))
    throw new Error('The native session changed or its agent ownership links changed during the Worker message read.')
  return { agentId, agentSessionId: before.agentSessionId, messages }
}

/**
 * Whether two Worker reads of one agent state the same owner: the same agent, Worker, provider, native session,
 * working directory, and child links. A read of the agent's messages is stable only between two such reads.
 */
export function sameAgentOwnership(first: AgentInfo, second: AgentInfo): boolean {
  return first.id === second.id && first.workerId === second.workerId && first.agentProvider === second.agentProvider
    && first.agentSessionId === second.agentSessionId && first.workingDir === second.workingDir
    && first.parentAgentId === second.parentAgentId && first.rootAgentId === second.rootAgentId
    && first.spawnSpanId === second.spawnSpanId && first.providerChildKey === second.providerChildKey
}

/**
 * Whether `seq` is a sequence that the Worker allocates for a stored message. The Worker allocates each sequence as
 * `message_seq_hwm + 1` from a high-water that starts at 0, and the resume clone copies sequences that are already
 * allocated, so a stored sequence is 1 or more. A list response uses 0 only to state "no message".
 */
export function isStoredMessageSeq(seq: bigint): boolean {
  return seq >= 1n
}

/**
 * Read every stored Worker message of one agent, oldest first, page by page.
 *
 * These pages fail the read:
 * - A page that repeats a message ID.
 * - A page that does not advance the cursor.
 * - A page that holds a sequence below 1, which the Worker never allocates.
 * - A page that claims another page with no message.
 *
 * The read does not check the agent identity. `readNativeMessageSnapshot` adds that check.
 *
 * `onPage` receives each page as the Worker returned it, before the checks, so a diagnostic caller keeps the bytes
 * of a page that then fails.
 */
export async function readAllAgentMessages(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
  onPage?: (messages: readonly AgentChatMessage[]) => void,
): Promise<AgentChatMessage[]> {
  if (agentId.trim() === '')
    throw new Error('The Worker message read requires a nonempty agent ID.')
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
    onPage?.(response.messages)
    if (response.messages.length === 0 && response.hasMore)
      throw new Error('The Worker message page is empty but claims another page.')
    for (const message of response.messages) {
      if (message.id.trim() === '' || seen.has(message.id))
        throw new Error('The Worker message page contains an absent or duplicate message ID.')
      if (cursor !== undefined && message.seq <= cursor)
        throw new Error('The Worker message cursor did not advance.')
      if (!isStoredMessageSeq(message.seq))
        throw new Error(`The Worker message page contains sequence ${message.seq}, a sequence below 1. The Worker allocates each sequence from 1.`)
      seen.add(message.id)
      messages.push(message)
      cursor = message.seq
    }
    if (!response.hasMore)
      return messages
  }
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

/**
 * Select the Worker rows that hold the exact text in one decoded string value of the content or the supplement.
 * The search reads no provider field, so it can count every row that carries a unique marker.
 */
export function nativeMessagesHoldingText(messages: readonly AgentChatMessage[], text: string): AgentChatMessage[] {
  if (text.trim() === '')
    throw new Error('The native message search requires nonempty text.')
  return messages.filter(message => [nativeMessageBody(message), nativeMessageSupplement(message)]
    .some(value => jsonStringValues(value).some(item => item.includes(text))))
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
