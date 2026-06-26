import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { isOptimisticLocal } from './chatReconcile'

/**
 * Pure seq-ordering core for the windowed chat store: the single home for the
 * "optimistic locals (seq 0n) always trail the server messages" invariant that the
 * full-window replace and both trims depend on. Extracted from chat.store so the
 * hardest windowing rule is unit-testable without standing up the reactive store --
 * every function here takes plain `AgentChatMessage[]` and returns a value, reading
 * no store state. Mirrors the chatReconcile / chatSpanIndex leaf extractions.
 */

/**
 * Index just past the last server message in `messages`, i.e. excluding any
 * trailing optimistic local messages (seq 0n) that are always pinned to the
 * tail. Server messages occupy `[0, serverMessageEnd)`.
 */
export function serverMessageEnd(messages: AgentChatMessage[]): number {
  let end = messages.length
  while (end > 0 && isOptimisticLocal(messages[end - 1]))
    end--
  return end
}

/**
 * Index of the first server message (seq != 0n) in `messages`, i.e. skipping any
 * leading optimistic locals. Mirror of `serverMessageEnd` for the head of the
 * list; returns `messages.length` when there are no server messages at all.
 */
export function serverMessageStart(messages: AgentChatMessage[]): number {
  let start = 0
  while (start < messages.length && isOptimisticLocal(messages[start]))
    start++
  return start
}

/**
 * The first SERVER message's seq (skipping leading optimistic locals, seq 0n), or
 * `undefined` when the window holds no server message. The single home for "the head
 * of the loaded server range", built on serverMessageStart so the "seq 0n locals are
 * not the head" rule lives in one place. Returns `undefined` (not 0n) for an
 * all-locals window, so a caller can distinguish "no server head yet" from a genuine
 * head at seq 0 -- unlike the store's getFirstSeq, which collapses both to 0n.
 */
export function firstServerSeq(messages: AgentChatMessage[]): bigint | undefined {
  const start = serverMessageStart(messages)
  return start < messages.length ? messages[start].seq : undefined
}

/**
 * Whether a loaded SERVER row at `seq` is a phantom to reap when reconciling the window
 * to the authoritative tail: it sits in the `(latestSeq, reapCeilingSeq]` band -- beyond
 * the authoritative tail (a deletion the client missed while disconnected), but NOT a
 * live arrival that raced in during catch-up. A row at or below `latestSeq` is real; a
 * row ABOVE `reapCeilingSeq` (when a ceiling is set) post-dates the catch-up START tail,
 * so it can't be a missed deletion and is exempt. With NO ceiling (CatchUpStart, before
 * any live arrival can be in the window), every row beyond `latestSeq` is a phantom. The
 * one home for the band math `reapPhantomRows` and the reconcile reason about, so the
 * "exempt live arrivals above the ceiling" rule can't be re-derived inconsistently.
 */
export function isReapablePhantom(seq: bigint, latestSeq: bigint, reapCeilingSeq?: bigint): boolean {
  return seq > latestSeq && (reapCeilingSeq === undefined || seq <= reapCeilingSeq)
}

/**
 * The last SERVER message's seq (the row just before any trailing optimistic
 * locals, seq 0n), or `undefined` when the window holds no server message. Mirror
 * of `firstServerSeq` for the tail of the loaded server range, built on
 * serverMessageEnd so the "seq 0n locals are not the tail" rule lives in one
 * place. Returns `undefined` (not 0n) for an all-locals window, so a caller can
 * distinguish "no server tail yet" from a genuine tail at seq 0 -- unlike the
 * store's getLastSeq, which collapses both to 0n.
 */
export function lastServerSeq(messages: AgentChatMessage[]): bigint | undefined {
  const end = serverMessageEnd(messages)
  return end > 0 ? messages[end - 1].seq : undefined
}

/**
 * Insert `message` (a server message, seq != 0n) into `list`, keeping server
 * messages ordered ascending by seq while leaving trailing optimistic locals
 * (seq 0n) pinned at the tail. Returns a new array; does NOT dedup (callers
 * that can receive an existing seq must check first).
 */
export function insertServerBySeq(list: AgentChatMessage[], message: AgentChatMessage): AgentChatMessage[] {
  const end = serverMessageEnd(list)
  // Fast path: newer than the last server message.
  if (end === 0 || message.seq > list[end - 1].seq)
    return [...list.slice(0, end), message, ...list.slice(end)]
  // Slow path: binary-insert among server messages [0, end).
  let lo = 0
  let hi = end
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    if (list[mid].seq < message.seq)
      lo = mid + 1
    else
      hi = mid
  }
  return [...list.slice(0, lo), message, ...list.slice(lo)]
}

/**
 * Concatenate server messages with their trailing optimistic locals (seq 0n),
 * which always pin to the tail AFTER the server budget. Returns `server`
 * unchanged when there are no locals -- so a no-local window update keeps the
 * array reference and skips the spread allocation. The single home for the
 * "optimistic locals trail the server messages" rule the full-window replace
 * (applyMessages) and both trims (trimNewestEnd / trimOldestEnd) all repeat.
 */
export function withTrailingLocals(server: AgentChatMessage[], locals: AgentChatMessage[]): AgentChatMessage[] {
  return locals.length > 0 ? [...server, ...locals] : server
}

/**
 * Of the spanIds carried by `dropped` rows, the ones safe to prune from the
 * command-stream buffers: a span stays alive while any SURVIVING row still
 * references it. A tool_use opener and its tool_result share one spanId, so a
 * trim boundary that splits the pair (opener dropped, result kept, or vice
 * versa) must NOT wipe the stream the surviving member still renders -- the same
 * surviving-row rule removeMessage and the reseq-beyond-window drop already
 * apply. Deduped, insertion-order preserving.
 */
export function prunableDroppedSpanIds(
  dropped: AgentChatMessage[],
  survivors: AgentChatMessage[],
): string[] {
  const surviving = new Set<string>()
  for (const m of survivors) {
    if (m.spanId)
      surviving.add(m.spanId)
  }
  const result: string[] = []
  const seen = new Set<string>()
  for (const m of dropped) {
    const s = m.spanId
    if (s && !surviving.has(s) && !seen.has(s)) {
      seen.add(s)
      result.push(s)
    }
  }
  return result
}

/**
 * Compute the window after inserting a FRESH (new-id) message, dropping the
 * reconciled optimistic local first. Returns the next array and whether
 * `message` was actually inserted: the seq dedup DISCARDS a server message
 * whose seq already exists under a different id, and the span index must not be
 * told about a discarded message (it would point a span slot at a row absent
 * from the window). Pure -- no store reads/writes -- so the caller drives the
 * span-index decision off `inserted` instead of re-scanning committed state.
 */
export function applyFreshMessage(
  prev: AgentChatMessage[],
  message: AgentChatMessage,
  reconciledLocalId: string | undefined,
): { next: AgentChatMessage[], inserted: boolean } {
  // Drop the reconciled local first, then insert in seq order below. The echo's
  // real seq must land among the server messages, NOT at the local's old index:
  // when an earlier send is still pending and a LATER send echoes first,
  // substituting in place would strand the earlier local (seq 0n) between two
  // server messages, breaking the "optimistic locals always trail" invariant
  // that serverMessageEnd / insertServerBySeq / trimOldestEnd depend on. A
  // reconciled echo always has seq != 0n, so it never re-enters the local-append
  // branch below.
  let base = prev
  if (reconciledLocalId) {
    const localIdx = prev.findIndex(m => m.id === reconciledLocalId)
    if (localIdx !== -1)
      base = [...prev.slice(0, localIdx), ...prev.slice(localIdx + 1)]
  }

  // Local (optimistic) messages have seq === 0n and always go at the end.
  if (isOptimisticLocal(message))
    return { next: [...base, message], inserted: true }

  // Dedup: skip if a server message with this exact seq already exists.
  const serverEnd = serverMessageEnd(base)
  for (let i = serverEnd - 1; i >= 0; i--) {
    if (base[i].seq === message.seq)
      return { next: base, inserted: false }
  }

  return { next: insertServerBySeq(base, message), inserted: true }
}

/**
 * Whether every fetched `older` row sits strictly below the window head -- the
 * precondition that lets mergeWindow's 'older' side prepend in O(1) without breaking seq
 * order. The head is the lowest server seq in `base` (optimistic locals at seq 0n are
 * pinned to the tail, never the head); an empty / all-local base has no head to violate.
 * Locals never appear in a fetched page, but seq 0n is ignored defensively.
 */
function olderRowsPrecedeWindowHead(older: AgentChatMessage[], base: AgentChatMessage[]): boolean {
  const headSeq = base.find(m => m.seq !== 0n)?.seq
  if (headSeq === undefined)
    return true
  return older.every(m => m.seq === 0n || m.seq < headSeq)
}

/**
 * Compute the window after merging a fetched `older`/`newer` page into `prev`,
 * dropping any reconciled optimistic locals (`reconciledLocalIds`). Pure -- no
 * store reads/writes -- so the dedup / reseq-collision / seq-ordering rules are
 * unit-testable on plain arrays; the caller (mergeFetchedMessages) drives the
 * reactive side effects (span reindex, command-stream prune, side-state reclaim)
 * off the committed result.
 */
export function mergeWindow(
  prev: AgentChatMessage[],
  fetched: AgentChatMessage[],
  side: 'older' | 'newer',
  reconciledLocalIds: Set<string>,
): AgentChatMessage[] {
  const prevById = new Map(prev.map(m => [m.id, m]))
  // A fetched row updates the window UNLESS an in-window row already holds it under
  // the SAME id AND seq (truly unchanged -- skip it to keep the existing store proxy
  // so its MessageBubble + local UI state survive). A BRAND-NEW id is always incoming.
  // A reseq the scrolled-away client missed reuses a STABLE id under a NEW seq
  // (notification consolidation reassigns MAX(seq)+1) and so is also incoming.
  const newMsgs = fetched.filter((m) => {
    const existing = prevById.get(m.id)
    return existing ? existing.seq !== m.seq : true
  })
  if (newMsgs.length === 0 && reconciledLocalIds.size === 0)
    return prev
  // Build the surviving base by dropping, from the window: every reconciled local;
  // the stale same-id copy of each reseq'd/updated row (so it isn't duplicated under
  // one id-keyed virtualizer slot); AND -- the "replace" rule -- any in-window row
  // whose seq a NEW id now claims. A reseq the client missed reassigned that seq to a
  // different id, so the incoming row authoritatively owns it and the stale occupant
  // goes (the alternative, dropping the newcomer, lost a real message). Optimistic
  // locals (seq 0n) are exempt from the seq-collision drop -- they all share 0n and
  // stay pinned to the tail. On the 'older' side this drop is inert: an older page's
  // seqs are all below the window head, so they never collide with a loaded row.
  const newIds = new Set(newMsgs.map(m => m.id))
  const newSeqs = new Set(newMsgs.filter(m => m.seq !== 0n).map(m => m.seq))
  const collidesNewSeq = (m: AgentChatMessage) => m.seq !== 0n && newSeqs.has(m.seq) && !newIds.has(m.id)
  const dropsExisting = reconciledLocalIds.size > 0
    || prev.some(m => newIds.has(m.id) || collidesNewSeq(m))
  const base = dropsExisting
    ? prev.filter(m => !reconciledLocalIds.has(m.id) && !newIds.has(m.id) && !collidesNewSeq(m))
    : prev
  if (newMsgs.length === 0)
    return base
  if (side === 'older') {
    // An older page's seqs are all strictly below the window head (a BEFORE-anchored
    // fetch; reseq only ever moves a row to a HIGHER seq), so a plain prepend keeps the
    // window seq-ascending -- the deliberate O(1) hot path. That invariant is a caller
    // contract the fetch doesn't enforce, so guard it: in dev/test ASSERT (surface a
    // future regression -- an overlapping page or a reseq-to-lower -- loudly), and in
    // production fall back to the same seq-ordered insert the 'newer' side uses so the
    // window can never silently lose the ordering serverMessageEnd / the binary searches
    // depend on. The fallback fires only on a violation, so the normal path stays O(1).
    if (olderRowsPrecedeWindowHead(newMsgs, base))
      return [...newMsgs, ...base]
    if (import.meta.env.DEV)
      throw new Error('mergeWindow: an older page overlaps the window head -- the older-side prepend would break seq ordering')
    return newMsgs.reduce((acc, m) => insertServerBySeq(acc, m), base)
  }
  // Insert each new row in SEQ order, keeping trailing optimistic locals (seq 0n)
  // pinned to the tail. A normal forward page is strictly newer than the window, so
  // insertServerBySeq's fast path just appends at the server tail (identical to the
  // prior blind splice). A reseq whose new seq lands among existing rows -- the
  // collision case the id-keyed `newMsgs` filter now admits -- is placed in order
  // rather than appended out of sequence, so the window stays seq-ascending for
  // serverMessageEnd / the binary searches.
  return newMsgs.reduce((acc, m) => insertServerBySeq(acc, m), base)
}
