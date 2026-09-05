import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'

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

/** Test whether every sequence in `list` is greater than the sequence before it. */
function ascendsBySeq(list: AgentChatMessage[]): boolean {
  for (let index = 1; index < list.length; index++) {
    if (list[index].seq <= list[index - 1].seq)
      return false
  }
  return true
}

/**
 * Merge two seq-ascending lists into one new ascending array.
 *
 * The output keeps every row of both inputs. On an equal sequence the function
 * writes the incoming row before the base row, which is the order that
 * {@link insertMessageBySeq} produces for the same pair.
 *
 * One pass replaces a fold over insertMessageBySeq, which allocates a fresh
 * array for each incoming row. A 50-row page merged into a 1200-row window
 * copies about 59,000 elements that way; this copies 1250.
 */
function mergeAscendingBySeq(base: AgentChatMessage[], incoming: AgentChatMessage[]): AgentChatMessage[] {
  const merged = Array.from<AgentChatMessage>({ length: base.length + incoming.length })
  let baseIndex = 0
  let incomingIndex = 0
  let out = 0
  while (baseIndex < base.length && incomingIndex < incoming.length) {
    merged[out++] = incoming[incomingIndex].seq <= base[baseIndex].seq
      ? incoming[incomingIndex++]
      : base[baseIndex++]
  }
  while (baseIndex < base.length)
    merged[out++] = base[baseIndex++]
  while (incomingIndex < incoming.length)
    merged[out++] = incoming[incomingIndex++]
  return merged
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
  // Both lists are normally ascending by a unique seq -- `base` is the window and
  // `incoming` is one server page -- so a single two-pointer pass merges them.
  // A list that breaks that precondition falls back to the repeated ordered
  // insert, which sorts an arbitrary input at the cost of one array per row.
  if (ascendsBySeq(base) && ascendsBySeq(incoming))
    return mergeAscendingBySeq(base, incoming)
  return incoming.reduce((result, message) => insertMessageBySeq(result, message), base)
}
