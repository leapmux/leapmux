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
import { createSignal, untrack } from 'solid-js'
import { getWorkerSystemInfo } from '~/api/workerRpc'
import { createInflightCache } from '~/lib/inflightCache'
import { shallowEqualExcept } from '~/lib/shallowEqual'
import { getWorkerInfo, setWorkerInfo } from '~/lib/workerInfoCache'

type InfoMap = Record<string, WorkerInfo>

/**
 * How long a cached snapshot stays "fresh enough" to skip the round trip
 * on dialog open. System info (homeDir, OS, version) is slow-changing —
 * a one-minute TTL is short enough to pick up worker restarts but long
 * enough that opening three dialogs back-to-back only forks one RPC per
 * worker. The localStorage entry survives past this window for offline-
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
  const [infoMap, setInfoMap] = createSignal<InfoMap>({})
  // Non-reactive memoization of the localStorage lookup: skips the
  // `getWorkerInfo` round trip for workers we've already seen. Only
  // POSITIVE hits are cached — negative reads always re-check localStorage
  // because a sibling store's `fetchWorkerInfo` may have written a fresh
  // value via `setWorkerInfo` between the first lookup and now. Caching
  // `null` poisoned this store's read forever and made the
  // cross-store sharing the comment advertises a lie. The cost: one
  // synchronous getItem per `workerInfo()` call for as-yet-unfetched
  // workers, paid until that worker's first successful fetch lands.
  const hydrated = new Map<string, WorkerInfo>()

  function readLocalStorage(workerId: string): WorkerInfo | null {
    const cached = hydrated.get(workerId)
    if (cached)
      return cached
    const fromStorage = getWorkerInfo(workerId) ?? null
    if (fromStorage)
      hydrated.set(workerId, fromStorage)
    return fromStorage
  }

  /**
   * Reactive read of cached info. On a miss we hydrate from
   * localStorage via the shared cache; the reactive `infoMap` is only
   * ever written from `fetchWorkerInfo` so a workspace full of tabs
   * reading `workerInfo(id)` for distinct ids does not cascade-notify
   * every other consumer of `infoMap()`.
   */
  function workerInfo(workerId: string): WorkerInfo | null {
    const map = infoMap()
    if (map[workerId])
      return map[workerId]
    return readLocalStorage(workerId)
  }

  async function fetchWorkerInfo(workerId: string): Promise<WorkerInfo | null> {
    // Warm path: a freshly-resolved info already sits in the reactive
    // map (this dialog open, or a sibling dialog open within the TTL
    // window).
    //
    // `untrack` keeps a reactive caller (e.g. `createEffect` →
    // `fetchWorkerInfo`) from subscribing to `infoMap` here — only the
    // call sites that explicitly read `workerInfo(id)` should
    // subscribe.
    const inMem = untrack(infoMap)[workerId]
    if (inMem && Date.now() - inMem.updatedAt < FRESH_TTL_MS)
      return inMem
    const cached = readLocalStorage(workerId)
    if (cached && Date.now() - cached.updatedAt < FRESH_TTL_MS) {
      if (inMem !== cached)
        setInfoMap(prev => ({ ...prev, [workerId]: cached }))
      return cached
    }
    // `pending.run` lives at module scope; the body runs once even
    // when multiple stores call in parallel. The localStorage write +
    // hydrated cache update happen inside the body (process-wide
    // side effects); the reactive map update happens outside so each
    // store's own `infoMap` signal is notified — the body closes over
    // only the first caller's setInfoMap.
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
      hydrated.set(workerId, info)
      setInfoMap((prev) => {
        const existing = prev[workerId]
        if (existing && shallowEqualExcept(existing, info, ['updatedAt']))
          return prev
        return { ...prev, [workerId]: info }
      })
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
