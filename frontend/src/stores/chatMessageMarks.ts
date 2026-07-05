import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'
import { createPerAgentStore } from './chatPerAgentStore'
import { isOptimisticLocalSeq } from './chatReconcile'

// ---------------------------------------------------------------------------
// Message-marks tracker
//
// Owns the per-agent set of "scroll-rail marks": the seqs of notable messages
// (user-typed inputs, control-request responses) the rail draws teal jump dots
// for, plus the agent's whole-history seq range. The frontend never holds the
// full conversation (a ~150-message sliding window), so these seqs -- and the
// min/max range -- come from the backend ListMessageMarks RPC (seed) and are then
// kept current incrementally from live events (noteMark / remove), exactly as
// chatLiveTail keeps the true tail. A dropped-beyond-window live message still
// records its mark, because addMessage calls noteMark BEFORE the window drop.
//
// Mirrors chatLiveTail's shape: layers domain reconcilers on a createPerAgentStore
// leaf. The leaf is a whole MessageMarks object replaced on every write (never
// mutated in place -- the empty value is shared across unseeded agents).
// ---------------------------------------------------------------------------

export interface SeqMark {
  seq: bigint
  type: MarkType
}

/**
 * The reactive scroll-rail data an agent exposes to the chat view: the marked seqs, the
 * effective whole-history seq range the rail's track spans, and the loaded window's bounds.
 * One named shape shared by the store's `getRailData` return and `ChatViewProps.rail`, so
 * the two can't drift. The range (minSeq/maxSeq) is already window-aware -- the store
 * resolves it via {@link resolveRailRange}, so the view passes it straight through.
 */
export interface ChatRailData {
  /** True once the marks RPC has seeded this agent (reveals the rail). */
  loaded: boolean
  /** Whole-history lowest seq the rail's seq-space track starts at (window-aware). */
  minSeq: bigint
  /** Whole-history highest seq the rail's seq-space track ends at (window-aware). */
  maxSeq: bigint
  /** The marked seqs (teal jump dots). */
  marks: readonly SeqMark[]
  /**
   * The loaded window's first/last SERVER seq (skipping optimistic locals), or undefined
   * when the window holds no server row. The rail's thumb-drag uses these to gate in-window
   * live-scrolling; a drag mapping outside them only previews.
   */
  windowFirstSeq: bigint | undefined
  windowLastSeq: bigint | undefined
}

/** Inputs to {@link resolveRailRange}: the seeded range, the live tail, and the loaded window. */
export interface RailRangeInputs {
  /** RPC-seeded whole-history lowest seq (`MessageMarks.minSeq`, lowered live by noteMark). */
  seededMinSeq: bigint
  /** RPC-seeded whole-history highest seq at seed time (`MessageMarks.seedMaxSeq`). */
  seedMaxSeq: bigint
  /** liveTail's observed max (0n before liveTail has seen a message). */
  liveMaxSeq: bigint
  /** The loaded window's first/last SERVER seq (undefined for an all-locals / empty window). */
  windowFirstSeq: bigint | undefined
  windowLastSeq: bigint | undefined
  /** Whether older messages remain unfetched below the loaded window. */
  hasOlderMessages: boolean
}

/**
 * Resolve the rail's effective whole-history seq range from the seeded range, the live tail,
 * and the loaded window. The SINGLE home for the window-aware min/max rule (previously split
 * across the store's getRailData and two ChatView memos, which could silently drift):
 *
 *  - min: when the oldest page is loaded, the window HEAD is the exact whole-history min, so
 *    the rail's floor tracks a delete of the oldest without waiting for a reseed; otherwise
 *    the RPC-seeded min (older history exists off-window).
 *  - max: liveTail is authoritative once populated -- it rises as the stream grows past the
 *    loaded window AND falls when the tail is deleted -- with the seed max as the pre-liveTail
 *    fallback (max(liveMax, seedMax) would instead pin a deleted tail's phantom slot at the
 *    stale seed max). But the window TAIL wins when it has outrun both, so the thumb keeps
 *    shrinking as rows persist past the window before liveTail catches up.
 */
export function resolveRailRange(inp: RailRangeInputs): { minSeq: bigint, maxSeq: bigint } {
  const seededOrLiveMax = inp.liveMaxSeq > 0n ? inp.liveMaxSeq : inp.seedMaxSeq
  return {
    minSeq: !inp.hasOlderMessages ? (inp.windowFirstSeq ?? inp.seededMinSeq) : inp.seededMinSeq,
    maxSeq: inp.windowLastSeq !== undefined && inp.windowLastSeq > seededOrLiveMax
      ? inp.windowLastSeq
      : seededOrLiveMax,
  }
}

export interface MessageMarks {
  /** True once the seed RPC has populated this agent at least once. */
  loaded: boolean
  /** Whole-history lowest seq (0n = empty / unseeded). */
  minSeq: bigint
  /** Whole-history highest seq at seed time (chatLiveTail supersedes it live). */
  seedMaxSeq: bigint
  /** Marked seqs, ascending, deduped. */
  marks: readonly SeqMark[]
}

const EMPTY_MARKS: MessageMarks = { loaded: false, minSeq: 0n, seedMaxSeq: 0n, marks: [] }

/**
 * Insert `{seq, type}` keeping `marks` ascending by seq. Returns the SAME array
 * reference when `seq` is already present (idempotent re-note of a re-broadcast
 * message), so callers can skip a no-op reactive write.
 */
export function insertMarkSorted(marks: readonly SeqMark[], seq: bigint, type: MarkType): readonly SeqMark[] {
  const idx = lowerBoundBySeq(marks, seq)
  if (idx < marks.length && marks[idx].seq === seq)
    return marks
  const next = marks.slice()
  next.splice(idx, 0, { seq, type })
  return next
}

/**
 * Remove the mark at `seq`. Returns the SAME array reference when `seq` is absent,
 * so callers can skip a no-op reactive write.
 */
export function removeMarkAt(marks: readonly SeqMark[], seq: bigint): readonly SeqMark[] {
  const idx = lowerBoundBySeq(marks, seq)
  if (idx >= marks.length || marks[idx].seq !== seq)
    return marks
  const next = marks.slice()
  next.splice(idx, 1)
  return next
}

/** Ascending, deduped copy of a seeded marks list (defensive against a mis-ordered RPC). */
function normalizeSeed(marks: readonly SeqMark[]): SeqMark[] {
  let alreadySorted = true
  for (let i = 1; i < marks.length; i++) {
    if (marks[i - 1].seq > marks[i].seq) {
      alreadySorted = false
      break
    }
  }
  const sorted = alreadySorted ? marks : [...marks].sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0))
  const out: SeqMark[] = []
  for (const m of sorted) {
    if (out.length > 0 && out[out.length - 1].seq === m.seq)
      continue
    out.push({ seq: m.seq, type: m.type })
  }
  return out
}

export function createMessageMarksStore() {
  const base = createPerAgentStore<MessageMarks>(EMPTY_MARKS)
  // Per-agent "live mutation revision", bumped by noteMark/remove on every REAL change (never a
  // no-op) so a concurrent loadMessageMarks can detect a mark change that raced its in-flight seed
  // RPC. Owning it HERE -- rather than the caller bumping after a returned bool -- makes it
  // mechanically impossible for a mark mutation to skip the bump. seed() deliberately does NOT
  // bump: it is the snapshot being raced against, not a live change.
  const liveRevisions = new Map<string, number>()
  const bump = (agentId: string) => {
    liveRevisions.set(agentId, (liveRevisions.get(agentId) ?? 0) + 1)
  }

  return {
    /** The reactive id -> MessageMarks map (read by id for reactivity). */
    get byAgent() {
      return base.byAgent
    },
    /** The marks state for an agent (EMPTY_MARKS when unseeded; treat as read-only). */
    get: base.get,
    /**
     * The per-agent live-mutation revision: a monotonically-increasing count bumped by every real
     * noteMark/remove. loadMessageMarks reads it before and after its seed RPC to detect a mark
     * change that raced the fetch (see the seed-race retry there). 0 for an agent never mutated.
     */
    liveRevision(agentId: string): number {
      return liveRevisions.get(agentId) ?? 0
    },
    /**
     * Replace an agent's marks from a ListMessageMarks response. An INDETERMINATE range
     * (min/max unset -- the worker couldn't read the seq range on a DB error) keeps the
     * prior value rather than trusting a bogus 0.
     */
    seed(agentId: string, marks: readonly SeqMark[], minSeq: bigint | undefined, maxSeq: bigint | undefined) {
      const cur = base.get(agentId)
      // Preserve any already-noted mark BEYOND this snapshot's horizon (seq > maxSeq): a
      // live message can be noteMark'd from a broadcast in the window between the worker
      // reading ListMessageMarks and this response applying (e.g. a send landing during a
      // reconnect reseed). Since maxSeq is the snapshot's MAX(seq), such a mark is provably
      // newer than the snapshot and absent from `marks`; a wholesale replace would drop its
      // dot until the next reseed. Marks at/below maxSeq stay the snapshot's authority, so a
      // delete that happened while disconnected is still healed. When maxSeq is indeterminate
      // (unset) the horizon is unknown, so the preserve is skipped and the fresh snapshot is
      // trusted wholesale (a beyond-horizon race mark is dropped); loadMessageMarks schedules a
      // retry to heal it once a good horizon returns.
      const freshBeyondSnapshot = maxSeq !== undefined ? cur.marks.filter(m => m.seq > maxSeq) : []
      base.set(agentId, {
        // `loaded` is what reveals the rail. Claim it only once we hold a usable range:
        // a FIRST seed whose min/max came back indeterminate (unset, a worker DB error) would
        // otherwise install real marks against the bogus 0n floor kept below and
        // mis-position every dot. Stay hidden until a good reseed heals it; once loaded, a
        // later indeterminate reseed keeps the prior range and stays loaded.
        loaded: cur.loaded || (minSeq !== undefined && maxSeq !== undefined),
        minSeq: minSeq !== undefined ? minSeq : cur.minSeq,
        seedMaxSeq: maxSeq !== undefined ? maxSeq : cur.seedMaxSeq,
        marks: normalizeSeed([...marks, ...freshBeyondSnapshot]),
      })
    },
    /**
     * Record a live message's mark. Ignores optimistic locals (seq 0n) and unmarked
     * rows. Idempotent for a re-broadcast seq. Lowers minSeq when the agent was empty
     * (0n) or the seq precedes the recorded min, so the rail range always covers it.
     * Bumps the live-mutation revision ONLY on a real change -- a no-op re-note (a
     * re-broadcast seq, an optimistic local, an unmarked row) must not perturb the
     * concurrent-seed race detector.
     */
    noteMark(agentId: string, seq: bigint, type: MarkType): void {
      if (isOptimisticLocalSeq(seq) || type === MarkType.UNSPECIFIED)
        return
      const cur = base.get(agentId)
      const nextMarks = insertMarkSorted(cur.marks, seq, type)
      const nextMin = cur.minSeq === 0n || seq < cur.minSeq ? seq : cur.minSeq
      if (nextMarks === cur.marks && nextMin === cur.minSeq)
        return
      base.set(agentId, { ...cur, minSeq: nextMin, marks: nextMarks })
      bump(agentId)
    },
    /**
     * Drop the mark at `seq` (no-op when absent). Called on message delete / phantom
     * reap. Bumps the live-mutation revision only on a real removal -- an unmarked seq
     * is a no-op and must not perturb the concurrent-seed race detector.
     */
    remove(agentId: string, seq: bigint): void {
      const cur = base.get(agentId)
      const nextMarks = removeMarkAt(cur.marks, seq)
      if (nextMarks === cur.marks)
        return
      base.set(agentId, { ...cur, marks: nextMarks })
      bump(agentId)
    },
    /** Drop an agent's marks (and its live-revision counter) entirely when the agent is closed. */
    forget(agentId: string) {
      base.remove(agentId)
      liveRevisions.delete(agentId)
    },
  }
}

export type MessageMarksStore = ReturnType<typeof createMessageMarksStore>
