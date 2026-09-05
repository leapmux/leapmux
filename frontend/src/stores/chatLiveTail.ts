import { createPerAgentStore } from './chatPerAgentStore'

// ---------------------------------------------------------------------------
// Live-tail tracker
//
// Owns the per-agent "recorded live tail" seq: the highest message seq the client
// has OBSERVED on the server, INCLUDING messages the live-append guard dropped while
// scrolled away (so they aren't silently lost). The window's loaded tail can lag this
// value; `caughtUp` compares the two, and the history paginator forward-fills the gap.
//
// Extracted from the windowed store so the "true tail + caught-up" invariant -- bump
// up on a new persisted message, clamp down when a fetch settles short, and
// reconcile from authoritative snapshots. One tested unit owns these rules instead of
// deriving them again with
// `?? 0n` across the store and the paginator. Independent of the windowing invariants,
// so it owns its own reactive slice.
//
// The storage IS the per-agent spine: a single bigint per agent with a 0n empty
// (get / set / remove). Only the bump, settle, and authoritative reconcilers
// below are bespoke high-water logic.
// ---------------------------------------------------------------------------

export function createLiveTailTracker() {
  const base = createPerAgentStore<bigint>(0n)
  const get = base.get
  const setByAgent = base.set

  return {
    /** The reactive id -> recorded-live-tail-seq map (read by id for reactivity). */
    get byAgent() {
      return base.byAgent
    },
    /** The recorded live tail for an agent (0n when none observed). */
    get,
    /**
     * Raise the recorded live tail when `seq` is a higher positive sequence. Called
     * for every message the store ingests before any beyond-window drop, so a message
     * dropped while scrolled away is still recorded as observed.
     */
    bump(agentId: string, seq: bigint) {
      if (seq > 0n && seq > get(agentId))
        setByAgent(agentId, seq)
    },
    /**
     * Whether the window (whose last loaded server seq is `windowTail`) has caught up
     * to the recorded live tail -- i.e. nothing observed sits past what's loaded. A 0n
     * recorded tail (nothing observed) is trivially caught up.
     */
    caughtUp(agentId: string, windowTail: bigint): boolean {
      return windowTail >= get(agentId)
    },
    /**
     * Clamp the recorded live tail DOWN to `windowTail` when a forward fetch reached
     * the server tail WITHOUT catching up -- the recorded seq is one the server can no
     * longer give us (a message broadcast then deleted, or a vanished gap), so leaving
     * it would wedge `caughtUp` false forever. Skips the clamp when the tail advanced
     * past `liveSeqAtEntry` since the fetch began: that higher seq came from a message
     * broadcast DURING the fetch and is genuinely reachable, so it must NOT be erased.
     */
    settleToWindow(agentId: string, liveSeqAtEntry: bigint, windowTail: bigint) {
      // Never clamp the recorded tail to 0n: an empty window tail means the server
      // range emptied during the fetch, not that
      // we caught up. Erasing it would make caughtUp trivially true and hide the
      // streaming tail while newer history still exists. An AUTHORITATIVE empty (an
      // empty LATEST response) is handled by resetToEmptyIfStale instead.
      if (windowTail === 0n)
        return
      if (get(agentId) <= liveSeqAtEntry)
        setByAgent(agentId, windowTail)
    },
    /**
     * Clamp the recorded live tail to EMPTY (0n) when an authoritative empty LATEST
     * response proves no messages exist (the whole history was deleted while scrolled
     * away). Unlike settleToWindow this DOES clamp to an empty window, but only when
     * the tail hasn't advanced past `liveSeqAtEntry` -- a mid-fetch broadcast raised a
     * genuinely-reachable seq the forward-fill will pull.
     */
    resetToEmptyIfStale(agentId: string, liveSeqAtEntry: bigint) {
      if (get(agentId) <= liveSeqAtEntry)
        setByAgent(agentId, 0n)
    },
    setAuthoritative(agentId: string, seq: bigint, reapCeilingSeq?: bigint) {
      const recorded = get(agentId)
      // Behind the authoritative tail: raise to it (the server has observed up to `seq`).
      if (seq > recorded) {
        setByAgent(agentId, seq)
        return
      }
      // Above it: lower ONLY a stale phantom in the reconciled (seq, ceiling] band. A
      // recorded tail above the ceiling is a live arrival and stays put.
      if (recorded > seq && (reapCeilingSeq === undefined || recorded <= reapCeilingSeq))
        setByAgent(agentId, seq > 0n ? seq : 0n)
    },
    /**
     * Drop an agent's recorded live tail entirely when the agent is closed. The
     * The reconcilers only raise or lower the bigint. They never
     * remove the key, so without this a long session leaks one entry per agent
     * ever observed. Called from the chat store's forgetAgent cleanup.
     */
    forget(agentId: string) {
      base.remove(agentId)
    },
  }
}

export type LiveTailTracker = ReturnType<typeof createLiveTailTracker>
