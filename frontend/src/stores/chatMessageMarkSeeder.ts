import type { MessageMarksStore } from './chatMessageMarks'
import { listMessageMarks as listMessageMarksRpc } from '~/api/workerRpc'

// ---------------------------------------------------------------------------
// Message-mark seeder (the seed-race machine)
//
// Owns the per-agent machine that SEEDS an agent's scroll-rail marks from the
// backend ListMessageMarks RPC and keeps re-seeding until the snapshot "sticks".
// The race-fencing sibling of createMessageMarksStore, which owns the marks DATA:
// the data store holds the reactive marks + the live-mutation revision; THIS holds
// the epoch fencing, the retry timers, and the bounded reschedule chain that drive a
// seed to completion against the three ways it can fail to stick -- a live mark racing
// the snapshot, an indeterminate range, and a transient RPC rejection (see `load`).
//
// Split out of the windowed chat store so the seed-race invariants -- a single global
// monotonic epoch that never reissues a live value, an immediate-retry loop plus a
// bounded delayed-reschedule chain, and a watchSignal teardown that cancels the chain
// -- live in one tested unit, mirroring how createMessageMarksStore (the marks data)
// and createLiveTailTracker (the true tail) each own their own slice. The store keeps
// the PUBLIC entry points: `loadMessageMarks` delegates to `load`, and forgetAgent
// calls `forget`.
// ---------------------------------------------------------------------------

export const MAX_MESSAGE_MARK_SEED_ATTEMPTS = 3
export const MESSAGE_MARK_SEED_RETRY_DELAY_MS = 250
// Cap on delayed reseeds chained from one fresh trigger, so a PERSISTENT failure (a worker
// whose seq-range query keeps erroring, or an RPC that keeps rejecting) self-limits to a short
// burst instead of polling every MESSAGE_MARK_SEED_RETRY_DELAY_MS for the whole session. A
// genuinely fresh trigger (initial load / reconnect catch-up / open) starts a new chain at 0.
export const MAX_MESSAGE_MARK_SEED_RESCHEDULES = 5

/** Dependencies the seeder needs from the store: the marks data store, and an optional RPC override. */
export interface MessageMarkSeederDeps {
  /** The marks DATA store this seeder seeds/reseeds (createMessageMarksStore). */
  marks: MessageMarksStore
  /**
   * The ListMessageMarks RPC. Optional: defaults to the real RPC, and is resolved LAZILY inside
   * `load` (never at construction) so building the store stays free of the RPC surface -- matching
   * the codebase's I/O-free store construction. Tests inject a fake here to drive the seed-race.
   */
  listMessageMarks?: typeof listMessageMarksRpc
}

/**
 * The scroll-rail mark seeder: the seed-race state machine behind the store's public
 * `loadMessageMarks`. See the module header for how it splits from the marks DATA.
 */
export function createMessageMarkSeeder(deps: MessageMarkSeederDeps) {
  const { marks } = deps

  // The epoch a loadMessageMarks call fences its late resolves against. Drawn from a single
  // monotonic counter (NOT a per-agent `+ 1`): forgetAgent deletes an agent's entry, so a
  // per-agent counter would restart at 0 and a close->reopen could mint the SAME epoch a
  // still-in-flight pre-close seed holds, defeating the cancel guard and letting the stale
  // RPC clobber the fresh window. A global counter never reissues a live epoch.
  const markSeedEpoch = new Map<string, number>()
  let markSeedEpochSeq = 0
  const markSeedRetryTimers = new Map<string, ReturnType<typeof setTimeout>>()
  const clearMarkSeedRetry = (agentId: string) => {
    const timer = markSeedRetryTimers.get(agentId)
    if (timer === undefined)
      return
    clearTimeout(timer)
    markSeedRetryTimers.delete(agentId)
  }

  /**
   * Seed (or re-seed) an agent's scroll-rail marks from the worker. Fire-and-forget:
   * a failure leaves the rail hidden/stale rather than blocking history load. Called
   * on initial load and again at catch-up complete to heal marks that changed (user
   * sends, deletes) while disconnected.
   *
   * `watchSignal` ties the seed to the CURRENT WatchEvents subscription (the same seam the
   * history fetches use, see beginHistoryFetch/linkWatchSignal): when a workspace switch /
   * worker reconnect tears the stream down, its abort cancels the in-flight ListMessageMarks
   * RPC and stops the retry chain from re-issuing against a worker the reader navigated away
   * from -- rather than polling it for up to MAX_MESSAGE_MARK_SEED_RESCHEDULES rounds. The
   * epoch guard still handles supersession by a fresh seed on the SAME subscription; the
   * signal handles the subscription itself going away.
   */
  async function load(workerId: string, agentId: string, watchSignal?: AbortSignal, rescheduleDepth = 0) {
    // Resolve the RPC HERE (not at construction) so building the store touches no RPC binding: the
    // injected fake if a test supplied one, else the real ListMessageMarks. See MessageMarkSeederDeps.
    const listMessageMarks = deps.listMessageMarks ?? listMessageMarksRpc
    // Clear any pending retry FIRST, then bail on an empty workerId or a torn-down subscription:
    // an empty workerId means the agent's worker/tab is gone (getAgentTab returned undefined), so a
    // reseed against it can't happen and the retry chain must STOP rather than keep polling a
    // worker the reader can no longer reach (see chat.store.test.ts "cancels a pending marks seed
    // retry when reseeded without a worker").
    clearMarkSeedRetry(agentId)
    if (!workerId || watchSignal?.aborted)
      return
    const epoch = ++markSeedEpochSeq
    markSeedEpoch.set(agentId, epoch)
    let revision = marks.liveRevision(agentId)
    // Re-arm the delayed reseed for THIS epoch (cancelled by a fresh call / forget via the
    // epoch guard, or a torn-down subscription via watchSignal), BOUNDED by
    // MAX_MESSAGE_MARK_SEED_RESCHEDULES so a persistent failure gives up instead of polling
    // forever. Reused by the three "seed didn't stick" cases: live updates kept racing the
    // snapshot, a first seed whose range came back indeterminate, and a transient RPC rejection.
    // `rescheduleDepth` counts the chain length from the fresh trigger; each reschedule
    // increments it, and a fresh external call starts a new chain at 0.
    const scheduleRetry = (reason: string) => {
      if (markSeedEpoch.get(agentId) !== epoch || watchSignal?.aborted)
        return
      if (rescheduleDepth + 1 >= MAX_MESSAGE_MARK_SEED_RESCHEDULES) {
        console.warn('message marks seed gave up after retries', { agentId, reason, rescheduleDepth })
        return
      }
      console.warn('message marks seed retry scheduled', { agentId, reason, rescheduleDepth })
      const timer = setTimeout(() => {
        markSeedRetryTimers.delete(agentId)
        if (markSeedEpoch.get(agentId) === epoch && !watchSignal?.aborted)
          void load(workerId, agentId, watchSignal, rescheduleDepth + 1)
      }, MESSAGE_MARK_SEED_RETRY_DELAY_MS)
      markSeedRetryTimers.set(agentId, timer)
    }
    try {
      for (let attempt = 0; attempt < MAX_MESSAGE_MARK_SEED_ATTEMPTS; attempt++) {
        const resp = await listMessageMarks(workerId, { agentId }, { signal: watchSignal })
        if (markSeedEpoch.get(agentId) !== epoch || watchSignal?.aborted)
          return
        const currentRevision = marks.liveRevision(agentId)
        if (currentRevision !== revision) {
          revision = currentRevision
          continue
        }
        marks.seed(
          agentId,
          resp.marks.map(m => ({ seq: m.seq, type: m.type })),
          resp.minSeq,
          resp.maxSeq,
        )
        // A FIRST seed whose range came back indeterminate (min/max unset: the worker's
        // seq-range subquery errored) leaves the rail hidden (seed keeps loaded=false), and the
        // live add/remove path can never reveal a never-seeded rail. Retry so a transient range
        // error self-heals instead of hiding the rail for the whole session.
        if (!marks.get(agentId).loaded) {
          scheduleRetry('first seed range indeterminate')
        }
        // An ALREADY-loaded agent reseeded with an indeterminate range stays loaded, but seed()
        // can no longer tell a beyond-horizon live mark (noteMark'd during the reseed race) from
        // a stale one, so it trusts the snapshot wholesale and drops that mark -- and nothing
        // re-notes it until the next reconnect reseed. Retry so a good horizon returns and seed()
        // heals the dropped dot promptly instead of leaving it missing for the session.
        else if (resp.minSeq === undefined || resp.maxSeq === undefined) {
          scheduleRetry('reseed range indeterminate')
        }
        return
      }
      scheduleRetry('live updates kept racing the seed')
    }
    catch (err) {
      // A subscription teardown aborts the in-flight RPC (watchSignal); that is a deliberate
      // cancellation, not a transient failure, so swallow it without logging or retrying -- the
      // fresh subscription's own seed takes over.
      if (watchSignal?.aborted)
        return
      // A transient RPC failure (a network blip / the worker briefly busy) must not hide the
      // rail for the whole session: retry, bounded like the other "seed didn't stick" cases,
      // so a one-off rejection self-heals but a persistent one stops instead of never revealing
      // the rail. (The indeterminate-range retry only covers a SUCCESSFUL response with a bad
      // range; an outright rejection never reaches the seed at all.)
      console.warn('failed to load message marks', { agentId, err })
      scheduleRetry('list message marks rpc failed')
    }
  }

  /**
   * Drop an agent's seed-race state when the agent is closed: cancel any pending retry
   * timer and delete its epoch entry (so a still-in-flight pre-close seed's late resolve
   * stays fenced -- see the global-counter note above). Called from the store's forgetAgent.
   */
  function forget(agentId: string) {
    clearMarkSeedRetry(agentId)
    markSeedEpoch.delete(agentId)
  }

  return { load, forget }
}

export type MessageMarkSeeder = ReturnType<typeof createMessageMarkSeeder>
