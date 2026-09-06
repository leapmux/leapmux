/**
 * Reactive store for worker system info fetched via E2EE channel.
 *
 * The module exports a single shared {@link workerInfoStore} singleton —
 * AppShell, dialog contexts, and any other consumer read through it so
 * concurrent fetches collapse onto one reactive `infoMap` and one
 * in-flight cache. {@link createWorkerInfoStore} is exported only so
 * unit tests can build isolated store instances with fresh state.
 */

import type { WorkerInfo } from '~/lib/workerInfoCache'
import { untrack } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { getWorkerSystemInfo } from '~/api/workerRpc'
import { onStorageAccountChange } from '~/lib/browserStorage'
import { createInflightCache } from '~/lib/inflightCache'
import { shallowEqualExcept } from '~/lib/shallowEqual'
import { getWorkerInfo, setWorkerInfo } from '~/lib/workerInfoCache'

/**
 * How long a cached snapshot stays "fresh enough" to skip the round trip
 * on dialog open. System info (homeDir, OS, version) is slow-changing —
 * a one-minute TTL is short enough to pick up worker restarts but long
 * enough that opening three dialogs back-to-back only forks one RPC per
 * worker. The stored entry survives past this window for offline-
 * display fallback; only the freshness probe gate is bounded here.
 */
const FRESH_TTL_MS = 60_000

// Process-wide coordination state shared across every WorkerInfoStore
// instance — RPC dedup and prefetch idempotency are session invariants
// that must survive store recreation. createWorkerInfoStore() builds a
// fresh reactive `infoMap` per caller (production singleton has one;
// tests build throwaway instances), but both fields below intentionally
// stay at module scope so a fetch initiated by store A still collapses
// with a concurrent fetch initiated by store B, and the prefetch guard
// doesn't lose its "already fanned out" memory on store rebuild.
const processSession = {
  pending: createInflightCache<string, WorkerInfo | null>(),
  prefetched: new Set<string>(),
}

/** Clears the prefetch guard so the next prefetch fan re-issues RPCs. */
export function resetOnlinePrefetch(): void {
  processSession.prefetched.clear()
}

/**
 * Atomic check-and-mark for the prefetch idempotency guard. Returns
 * true the first time a workerId is seen (caller should fan out an RPC),
 * false on every subsequent call until {@link resetOnlinePrefetch} is
 * invoked.
 */
export function shouldPrefetchOnline(workerId: string): boolean {
  if (processSession.prefetched.has(workerId))
    return false
  processSession.prefetched.add(workerId)
  return true
}

export interface WorkerInfoStore {
  /** Reactive read of cached info for a worker; null if not yet fetched. */
  workerInfo: (workerId: string) => WorkerInfo | null
  /** Force a refresh via E2EE; honors {@link FRESH_TTL_MS} and the in-flight cache. */
  fetchWorkerInfo: (workerId: string) => Promise<WorkerInfo | null>
  /** Convenience: cached homeDir, or empty string. */
  getHomeDir: (workerId: string) => string
  /** Convenience: cached OS, or undefined. */
  getOs: (workerId: string) => string | undefined
}

/**
 * Build a fresh store instance with its own reactive `infoMap`. Use for
 * test isolation; production code reads {@link workerInfoStore} instead.
 */
export function createWorkerInfoStore(): WorkerInfoStore {
  /**
   * Everything this store knows about a worker, keyed by id.
   *
   * ONE STRUCTURE, and a `createStore` rather than a `createSignal` because the
   * whole difficulty here is per-KEY notification. A signal holding one record
   * notifies every consumer of every OTHER worker on any write, so a workspace
   * full of tabs would re-render each other N times -- which is why this used to
   * be three structures: a signal for fetched rows, a plain Map for rows read
   * off disk, and a signal PER worker to notify about the second without
   * disturbing the first. A store's proxy subscribes a reader to the one key it
   * touched, so the library supplies what those three were built to fake.
   *
   * A row's `updatedAt` is what ranks two readings, so the source no longer has
   * to be tracked separately: a fetch stamps `Date.now()`, and a row read off
   * disk carries the stamp of the fetch that wrote it.
   */
  const [infoByWorker, setInfoByWorker] = createStore<Record<string, WorkerInfo>>({})
  // Workers whose read is in flight, so a column of rows asking for the same
  // id in one render issues ONE read rather than one each.
  const reading = new Set<string>()

  // `worker-info:` is an account-scoped family, so this cache belongs to the
  // account it was read for -- the rule `~/lib/browserStorage` states for any
  // module that mirrors one in memory. An in-tab account switch needs no reload,
  // so without this the next account reads the previous one's rows.
  // `reconcile` drops every key and notifies the readers of each, which is what
  // makes a still-mounted consumer re-ask rather than sit on a stale row.
  onStorageAccountChange(() => {
    setInfoByWorker(reconcile({}))
  })

  /**
   * A worker's record as a PLAIN COPY, never the store's own proxy.
   *
   * THE SPREAD IS LOAD-BEARING. `WorkspaceTabTree.workersProjection` holds the
   * previous reading and compares it field by field against the next one
   * (`workerProjectionsEqual`) to decide whether to rebuild the tree. Handing
   * out the proxy makes both sides the SAME live object, so every field compares
   * equal however much the record changed -- and the tree freezes showing the
   * row it first painted, which is exactly the defect that memo's comment
   * records fixing. `workerInfo.store.test.ts` pins it.
   */
  function snapshot(workerId: string): WorkerInfo | null {
    const row = infoByWorker[workerId]
    return row ? { ...row } : null
  }

  /**
   * Start a persisted read for `workerId` and publish it when it lands.
   *
   * The read is asynchronous (worker info is an unbounded family on the
   * unmirrored storage tier), so a cached name appears one microtask after the
   * first render rather than during it. Writing the store's own key is what
   * notifies the rows that already rendered without it, and only those: a
   * consumer of another worker is not disturbed.
   */
  function startPersistedRead(workerId: string): void {
    if (reading.has(workerId))
      return
    reading.add(workerId)
    void getWorkerInfo(workerId)
      .then((fromStorage) => {
        if (!fromStorage)
          return
        // A newer reading may have landed while this one was in flight -- a
        // fetch, or another consumer's read. `updatedAt` ranks them, so the
        // comparison no longer depends on knowing which cache each came from.
        const current = untrack(() => infoByWorker[workerId])
        if (current && current.updatedAt >= fromStorage.updatedAt)
          return
        setInfoByWorker(workerId, fromStorage)
      })
      // A failed read is a MISS, which this store already has an answer for.
      // `localStorageLoad` resolves the account key synchronously and throws for
      // a name it cannot resolve, which an `async` function turns into a
      // rejection -- so without this it would also be an unhandled one.
      .catch(() => {})
      // In a `finally`, so a rejected read releases the id too. Leaving it in
      // `reading` makes every later `workerInfo(id)` return early and answer
      // null for the rest of the session -- one read poisoning the store for
      // ever, which is the defect the negative-caching rule below exists for.
      .finally(() => {
        reading.delete(workerId)
      })
  }

  /**
   * Reactive read of cached info. On a miss it starts a persisted read and
   * answers null for now, then re-runs when that read lands.
   *
   * ONLY A POSITIVE HIT SHORT-CIRCUITS. A miss re-checks storage every time,
   * because a sibling store's `fetchWorkerInfo` may have written a fresh row
   * through `setWorkerInfo` since the last look. Caching the miss poisoned this
   * store's read for ever and made the cross-store sharing a lie.
   *
   * Reading `infoByWorker[workerId]` subscribes to THAT WORKER's key and nothing
   * else, so a workspace full of tabs reading distinct ids does not
   * cascade-notify every other consumer.
   */
  function workerInfo(workerId: string): WorkerInfo | null {
    const cached = snapshot(workerId)
    if (cached)
      return cached
    startPersistedRead(workerId)
    return null
  }

  async function fetchWorkerInfo(workerId: string): Promise<WorkerInfo | null> {
    // Warm path: a reading already in the store is fresh enough (this dialog
    // open, or a sibling dialog open within the TTL window).
    //
    // `untrack` keeps a reactive caller (e.g. `createEffect` →
    // `fetchWorkerInfo`) from subscribing here — only the call sites that
    // explicitly read `workerInfo(id)` should subscribe.
    const inMem = untrack(() => snapshot(workerId))
    if (inMem && Date.now() - inMem.updatedAt < FRESH_TTL_MS)
      return inMem
    // This one AWAITS the persisted read rather than starting it and moving
    // on: it is already an async function, and its answer decides whether to
    // spend a worker round trip.
    const cached = inMem ?? await getWorkerInfo(workerId)
    if (cached && Date.now() - cached.updatedAt < FRESH_TTL_MS) {
      setInfoByWorker(workerId, cached)
      return cached
    }
    // `pending.run` lives at module scope; the body runs once even when
    // multiple stores call in parallel. The persisted write happens inside the
    // body (a process-wide side effect); each store writes its OWN state
    // outside, because the body closes over only the first caller's setter.
    const info = await processSession.pending.run(workerId, async () => {
      try {
        const resp = await getWorkerSystemInfo(workerId)
        const next: WorkerInfo = {
          name: resp.name,
          os: resp.os,
          arch: resp.arch,
          homeDir: resp.homeDir,
          version: resp.version,
          commitHash: resp.commitHash,
          buildTime: resp.buildTime,
          updatedAt: Date.now(),
        }
        setWorkerInfo(workerId, next)
        return next
      }
      catch {
        return null
      }
    })
    if (info) {
      // `updatedAt` alone is not a change worth notifying about: a re-fetch that
      // confirms the same system info would otherwise re-run every reader of
      // this worker on the TTL cadence, for a stamp nothing displays.
      const existing = untrack(() => infoByWorker[workerId])
      if (!existing || !shallowEqualExcept(existing, info, ['updatedAt']))
        setInfoByWorker(workerId, info)
    }
    return info
  }

  function getHomeDir(workerId: string): string {
    return workerInfo(workerId)?.homeDir ?? ''
  }

  function getOs(workerId: string): string | undefined {
    return workerInfo(workerId)?.os
  }

  return { workerInfo, fetchWorkerInfo, getHomeDir, getOs }
}

/**
 * Process-wide singleton: every production consumer (AppShell, dialog
 * contexts, tile renderer) reads through this instance so a fetch
 * initiated by one consumer warms the cache for every other.
 */
export const workerInfoStore: WorkerInfoStore = createWorkerInfoStore()
