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
// up on a new server message, clamp down to the window when a fetch settles short,
// reconcile on delete -- lives in one tested unit instead of being re-derived with
// `?? 0n` across the store and the paginator. Independent of the windowing invariants,
// so it owns its own reactive slice (mirroring createMessageAnnotationStore et al.).
//
// The storage IS the per-agent spine: a single bigint per agent with a 0n empty
// (get / set / remove). Only the bump/settle/onDelete/setAuthoritative reconcilers
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
     * Raise the recorded live tail to `seq` when it is a SERVER seq (non-zero, so an
     * optimistic local at seq 0n never moves it) beyond the current high-water. Called
     * for every message the store ingests, BEFORE any beyond-window drop, so a message
     * dropped while scrolled away is still recorded as observed.
     */
    bump(agentId: string, seq: bigint) {
      if (seq !== 0n && seq > get(agentId))
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
      // range emptied DURING the fetch (a concurrent trim / messageDeleted), NOT that
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
    /**
     * Reconcile the recorded live tail when a message is deleted. When the deleted row
     * WAS the recorded tail, drop the high-water to the authoritative post-delete tail
     * (`newLatestSeq`), clamped at the window's new last seq (`windowTail`) so a
     * lagging/under-estimated value can never claim a tail BELOW a row still loaded
     * (which would make caughtUp falsely true). A delete elsewhere leaves it alone.
     *
     * `removedSeq` is the deleted row's seq IF it was loaded in the window (undefined
     * otherwise); `deletedSeq` is the seq the broadcast carries for an UNLOADED delete.
     */
    onDelete(agentId: string, opts: {
      removedSeq?: bigint
      deletedSeq?: bigint
      newLatestSeq?: bigint
      windowTail: bigint
    }) {
      const { removedSeq, deletedSeq, windowTail } = opts
      // Floor a reconciled tail at the window's last loaded seq: a lowered/lagging value
      // must NEVER claim a tail BELOW a row still loaded in the window (that would make
      // caughtUp falsely true). The one home for the floor both delete branches apply.
      const clampAtWindowFloor = (seq: bigint): bigint => (seq > windowTail ? seq : windowTail)
      // Normalize the indeterminate sentinel: the broadcast carries new_latest_seq = -1
      // when the worker couldn't read the tail (a DB error), which is NOT a real seq.
      // Treat it like "no authoritative tail carried" so we never lower the recorded
      // tail toward a bogus value (see AgentMessageDeleted.new_latest_seq).
      const newLatestSeq = opts.newLatestSeq !== undefined && opts.newLatestSeq >= 0n ? opts.newLatestSeq : undefined
      const recordedTail = get(agentId)
      if (removedSeq !== undefined && removedSeq !== 0n && removedSeq === recordedTail) {
        // Loaded tail deleted. Prefer the authoritative new tail; a local optimistic
        // delete (or an indeterminate broadcast) carries none, so fall back to the
        // window's new last seq -- which we can see, since the deleted row was loaded.
        const resolved = newLatestSeq ?? windowTail
        setByAgent(agentId, clampAtWindowFloor(resolved))
      }
      else if (removedSeq === undefined && deletedSeq !== undefined && deletedSeq !== 0n && deletedSeq === recordedTail) {
        // The deleted row was an UNLOADED beyond-window tail. With an authoritative new
        // tail, set it exactly; otherwise -- an INDETERMINATE -1 broadcast (a failed
        // worker MAX(seq) readback, normalized to undefined above) or no new_latest_seq
        // field at all -- fall back to deletedSeq - 1n. That is the PROVABLE ceiling of
        // the new tail, NOT a guess: we are in this branch only because deletedSeq ===
        // recordedTail, the highest seq the client has observed, and ordered broadcasts
        // rule out a higher UNobserved one (a later create/reseq would have bumped
        // recordedTail past deletedSeq before this delete). So deletedSeq - 1n can never
        // UNDER-report -- which would wrongly clear the "new messages below" affordance --
        // and a residual OVER-report (a seq gap below deletedSeq) self-heals via a later
        // forward-fill's settleToWindow clamp. Leaving the recorded tail at the
        // now-deleted seq instead would keep the affordance falsely lit forever: it can
        // never passively reach a seq we KNOW is gone. Clamped at the window's last seq so
        // a lagging value can't claim a tail below a loaded row.
        const lowered = newLatestSeq ?? (deletedSeq - 1n)
        setByAgent(agentId, clampAtWindowFloor(lowered))
      }
    },
    /**
     * Reconcile the recorded live tail to the AUTHORITATIVE server max seq the worker
     * reports at catch-up. Raises a lagging tail; also clamps DOWN a tail over-recorded
     * from a deletion the client missed while disconnected, so a stale high-water can't
     * wedge `caughtUp` false forever. But a recorded tail ABOVE `reapCeilingSeq` came
     * from a live broadcast that raced in DURING catch-up -- its seq exceeds the catch-up
     * START tail, so it post-dates replay and is genuinely reachable -- and is NEVER
     * lowered. (The watcher is registered BEFORE the worker reads the tail, so such a
     * frame CAN precede catch-up complete; the old "ordered before any live frame" note
     * missed that race.) No ceiling (CatchUpStart, before any live arrival) lowers any
     * stale tail above `seq`. Clamped non-negative.
     */
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
     * bump/settle/delete reconcilers only ever raise or lower the bigint, never
     * remove the key, so without this a long session leaks one entry per agent
     * ever observed. Called from the chat store's forgetAgent cleanup.
     */
    forget(agentId: string) {
      base.remove(agentId)
    },
  }
}

export type LiveTailTracker = ReturnType<typeof createLiveTailTracker>
