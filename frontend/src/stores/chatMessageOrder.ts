import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'

/** Index after the final message in the ordered transcript window. */
export function transcriptMessageEnd(messages: AgentChatMessage[]): number {
  return messages.length
}

/** Index of the first message in the ordered transcript window. */
export function transcriptMessageStart(_messages: AgentChatMessage[]): number {
  return 0
}

/** The first message sequence, or undefined for an empty window. */
export function firstMessageSeq(messages: AgentChatMessage[]): bigint | undefined {
  return messages[0]?.seq
}

/**
 * Test whether a row is in the catch-up phantom interval.
 *
 * A row above the optional ceiling arrived after catch-up started. The client
 * keeps that row.
 */
export function isReapablePhantom(seq: bigint, latestSeq: bigint, reapCeilingSeq?: bigint): boolean {
  return seq > latestSeq && (reapCeilingSeq === undefined || seq <= reapCeilingSeq)
}

/** The last message sequence, or undefined for an empty window. */
export function lastMessageSeq(messages: AgentChatMessage[]): bigint | undefined {
  return messages.at(-1)?.seq
}

/** Insert a message while sequence order remains ascending. */
export function insertMessageBySeq(list: AgentChatMessage[], message: AgentChatMessage): AgentChatMessage[] {
  if (list.length === 0 || message.seq > list[list.length - 1].seq)
    return [...list, message]
  const index = lowerBoundBySeq(list, message.seq)
  return [...list.slice(0, index), message, ...list.slice(index)]
}

/**
 * Return dropped span IDs that no surviving row uses.
 *
 * An opener and its result can share a span ID. The function keeps that span
 * while either row survives.
 */
export function prunableDroppedSpanIds(
  dropped: AgentChatMessage[],
  survivors: AgentChatMessage[],
): string[] {
  const surviving = new Set<string>()
  for (const message of survivors) {
    if (message.spanId)
      surviving.add(message.spanId)
  }
  const result: string[] = []
  const seen = new Set<string>()
  for (const message of dropped) {
    const spanId = message.spanId
    if (spanId && !surviving.has(spanId) && !seen.has(spanId)) {
      seen.add(spanId)
      result.push(spanId)
    }
  }
  return result
}

/**
 * Insert a new-ID message unless another row already owns its sequence.
 *
 * The same array reference reports a duplicate sequence.
 */
export function applyFreshMessage(
  previous: AgentChatMessage[],
  message: AgentChatMessage,
): { next: AgentChatMessage[], inserted: boolean } {
  const duplicateIndex = lowerBoundBySeq(previous, message.seq)
  if (duplicateIndex < previous.length && previous[duplicateIndex].seq === message.seq)
    return { next: previous, inserted: false }
  return { next: insertMessageBySeq(previous, message), inserted: true }
}

function olderRowsPrecedeWindowHead(older: AgentChatMessage[], base: AgentChatMessage[]): boolean {
  const headSeq = base[0]?.seq
  return headSeq === undefined || older.every(message => message.seq < headSeq)
}

/**
 * Merge one fetched page into an ordered transcript window.
 *
 * A stable ID with a new sequence replaces its stale copy. A new ID that owns
 * an existing sequence replaces the stale occupant of that sequence.
 */
export function mergeWindow(
  previous: AgentChatMessage[],
  fetched: AgentChatMessage[],
  side: 'older' | 'newer',
): AgentChatMessage[] {
  const previousByID = new Map(previous.map(message => [message.id, message]))
  const incoming = fetched.filter((message) => {
    const existing = previousByID.get(message.id)
    return existing ? existing.seq !== message.seq : true
  })
  if (incoming.length === 0)
    return previous

  const incomingIDs = new Set(incoming.map(message => message.id))
  const incomingSeqs = new Set(incoming.map(message => message.seq))
  const collides = (message: AgentChatMessage) =>
    incomingSeqs.has(message.seq) && !incomingIDs.has(message.id)
  const mustFilter = previous.some(message => incomingIDs.has(message.id) || collides(message))
  const base = mustFilter
    ? previous.filter(message => !incomingIDs.has(message.id) && !collides(message))
    : previous

  if (side === 'older') {
    if (olderRowsPrecedeWindowHead(incoming, base))
      return [...incoming, ...base]
    if (import.meta.env.DEV)
      throw new Error('mergeWindow: an older page overlaps the window head -- the older-side prepend would break seq ordering')
  }
  return incoming.reduce((result, message) => insertMessageBySeq(result, message), base)
}
