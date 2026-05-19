import type { WorkerInfo } from '~/lib/workerInfoCache'
import { createEffect, createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flush } from '../helpers/async'

const mockGetWorkerSystemInfo = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getWorkerSystemInfo: (...args: unknown[]) => mockGetWorkerSystemInfo(...args),
}))

// Imported after the mock so the store closure binds to the mocked RPC.
const { createWorkerInfoStore, workerInfoStore, resetOnlinePrefetch } = await import('~/stores/workerInfo.store')
const { getWorkerInfo, setWorkerInfo, clearWorkerInfo } = await import('~/lib/workerInfoCache')

function makeRespFor(id: string, overrides: Partial<WorkerInfo> = {}) {
  return {
    name: `worker-${id}`,
    os: 'linux',
    arch: 'x64',
    homeDir: `/home/${id}`,
    version: '1.0.0',
    commitHash: 'deadbeef',
    buildTime: '2026-01-01',
    ...overrides,
  }
}

// Each test uses a fresh workerId so the module-scope sharedPending /
// localStorage don't carry state across runs.
let nextId = 0
function uniqueWorkerId(): string {
  nextId++
  return `wid-${nextId}-${Math.random().toString(36).slice(2, 8)}`
}

beforeEach(() => {
  vi.clearAllMocks()
  // Module-scope `processSession.prefetched` Set persists across tests
  // and is the only cross-test leak the per-id `uniqueWorkerId` mitigation
  // doesn't cover (it survives even when each test allocates its own id).
  // Reset explicitly so the next test starts from a clean prefetch gate.
  resetOnlinePrefetch()
})

describe('workerInfoStore', () => {
  it('issues the RPC when localStorage is empty and caches the result', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)
      expect(info?.homeDir).toBe(`/home/${id}`)
      // Reactive accessor reflects the fetched info.
      expect(store.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      // localStorage hydrated for cross-store sharing.
      expect(getWorkerInfo(id)?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('skips the RPC when localStorage holds an entry within the freshness TTL', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      // Seed a fresh snapshot directly into localStorage.
      setWorkerInfo(id, {
        name: 'cached',
        os: 'darwin',
        arch: 'arm64',
        homeDir: '/home/cached',
        version: '0.9',
        commitHash: 'cafefeed',
        buildTime: '2025-12-01',
        updatedAt: Date.now(),
      })

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(mockGetWorkerSystemInfo).not.toHaveBeenCalled()
      expect(info?.homeDir).toBe('/home/cached')
      // Reactive map is hydrated from localStorage so downstream reads
      // pick up the cached value without an extra dance.
      expect(store.workerInfo(id)?.homeDir).toBe('/home/cached')
      dispose()
    })
  })

  it('issues the RPC when localStorage holds a stale entry past the freshness TTL', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      // Snapshot older than the 60 000 ms freshness window.
      setWorkerInfo(id, {
        name: 'stale',
        os: 'linux',
        arch: 'x64',
        homeDir: '/home/stale',
        version: '0.1',
        commitHash: 'old',
        buildTime: '2024-01-01',
        updatedAt: Date.now() - 5 * 60 * 1000,
      })
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id, { homeDir: '/home/fresh' }))

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)
      expect(info?.homeDir).toBe('/home/fresh')
      // localStorage is overwritten so the next dialog within the
      // freshness window will see the refreshed payload.
      expect(getWorkerInfo(id)?.homeDir).toBe('/home/fresh')
      dispose()
    })
  })

  it('collapses concurrent fetches across stores onto a single RPC', async () => {
    // The shared in-flight map lives at module scope: two dialogs each
    // with their own store closure must not duplicate the round trip.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      let resolveRpc: (v: ReturnType<typeof makeRespFor>) => void = () => {}
      mockGetWorkerSystemInfo.mockReturnValueOnce(
        new Promise((r) => { resolveRpc = r }),
      )

      const storeA = createWorkerInfoStore()
      const storeB = createWorkerInfoStore()
      const fetchA = storeA.fetchWorkerInfo(id)
      const fetchB = storeB.fetchWorkerInfo(id)

      // Only one RPC is in flight even though both stores called fetch.
      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)

      resolveRpc(makeRespFor(id))
      const [infoA, infoB] = await Promise.all([fetchA, fetchB])

      // Both stores receive the same payload and update their own
      // reactive infoMap.
      expect(infoA?.homeDir).toBe(`/home/${id}`)
      expect(infoB?.homeDir).toBe(`/home/${id}`)
      expect(storeA.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      expect(storeB.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('returns null on RPC failure and does not poison the localStorage cache', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockRejectedValueOnce(new Error('worker offline'))

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(info).toBeNull()
      // No localStorage write on failure — a future fetch attempt must
      // still hit the RPC instead of falling for a stale "success".
      expect(getWorkerInfo(id)).toBeNull()
      dispose()
    })
  })

  it('workerInfo() hydrates a fresh store from localStorage on first access', async () => {
    // Across a page reload (or a new dialog opening with a fresh store),
    // the reactive infoMap starts empty but localStorage already holds
    // the prior snapshot — workerInfo() must surface it transparently
    // (via its non-reactive hydration cache).
    await createRoot((dispose) => {
      const id = uniqueWorkerId()
      setWorkerInfo(id, {
        name: 'persisted',
        os: 'linux',
        arch: 'x64',
        homeDir: '/home/persisted',
        version: '1.2',
        commitHash: 'abc',
        buildTime: '2026-02-01',
        updatedAt: Date.now(),
      })

      const store = createWorkerInfoStore()
      expect(mockGetWorkerSystemInfo).not.toHaveBeenCalled()
      expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted')
      expect(store.getHomeDir(id)).toBe('/home/persisted')
      dispose()
    })
  })

  // The module-scope `workerInfoStore` is the singleton every production
  // consumer (AppShell, dialog contexts, tile renderer) reads through.
  // Two import paths must converge to the same object so a fetch
  // initiated by one consumer warms the cache for every other.
  it('module-scope workerInfoStore singleton is stable across imports', async () => {
    const a = workerInfoStore
    const b = (await import('~/stores/workerInfo.store')).workerInfoStore
    expect(a).toBe(b)
    // And the factory always builds a fresh, distinct instance — test
    // isolation must not see the singleton.
    expect(createWorkerInfoStore()).not.toBe(a)
  })

  it('singleton fetchWorkerInfo routes through the shared in-flight cache (no duplicate RPC)', async () => {
    // Two concurrent fetches against the singleton must collapse onto
    // one RPC; the in-flight cache lives at module scope.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      let resolveRpc: (v: ReturnType<typeof makeRespFor>) => void = () => {}
      mockGetWorkerSystemInfo.mockReturnValueOnce(
        new Promise((r) => { resolveRpc = r }),
      )

      const [fetchA, fetchB] = [
        workerInfoStore.fetchWorkerInfo(id),
        workerInfoStore.fetchWorkerInfo(id),
      ]
      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)

      resolveRpc(makeRespFor(id))
      const [infoA, infoB] = await Promise.all([fetchA, fetchB])
      expect(infoA?.homeDir).toBe(`/home/${id}`)
      expect(infoB?.homeDir).toBe(`/home/${id}`)
      // The singleton's reactive map also reflects the result, so a
      // dialog opened after this fetch reads through the cache.
      expect(workerInfoStore.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('fetchWorkerInfo() still notifies subscribers after a workerInfo() read hydrated from localStorage', async () => {
    // Regression guard for the non-reactive hydration cache. workerInfo()
    // now routes localStorage hits through a non-reactive Map, so a
    // careless refactor could end up shadowing future writes (subscriber
    // reads stale cached value forever). This test pins that:
    //   1. The initial workerInfo() read returns the localStorage value.
    //   2. The reactive subscription is still live — when fetchWorkerInfo
    //      lands a fresh payload, the subscriber re-runs and sees the new
    //      value from `infoMap`, not the cached one.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        setWorkerInfo(id, {
          name: 'stale-cached',
          os: 'linux',
          arch: 'x64',
          homeDir: '/home/stale',
          version: '1.0',
          commitHash: 'old',
          buildTime: '2025-01-01',
          // Past the freshness TTL so fetchWorkerInfo will fire an RPC.
          updatedAt: Date.now() - 5 * 60 * 1000,
        })

        const store = createWorkerInfoStore()
        const observed: (string | null | undefined)[] = []
        createEffect(() => {
          observed.push(store.workerInfo(id)?.homeDir)
        })
        await flush()
        expect(observed).toEqual(['/home/stale'])

        mockGetWorkerSystemInfo.mockResolvedValueOnce(
          makeRespFor(id, { homeDir: '/home/fresh' }),
        )
        await store.fetchWorkerInfo(id)
        await flush()

        // Subscriber observed the post-fetch value: the non-reactive
        // hydration cache did not block the reactive `infoMap` write.
        expect(observed).toEqual(['/home/stale', '/home/fresh'])
        dispose()
        done()
      })
    })
  })

  it('fetchWorkerInfo populates the hydrated cache so a later workerInfo() read survives a localStorage wipe', async () => {
    // The `hydrated` cache is shared between workerInfo() and
    // fetchWorkerInfo. A successful fetch must seed the cache so a
    // subsequent workerInfo() — say, after a sweep wiped localStorage
    // — still surfaces the prior result instead of re-hitting
    // localStorage and observing the gap. Pairs with the in-memory
    // short-circuit test below, which covers the reactive infoMap;
    // this one specifically covers the non-reactive hydration cache.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      await store.fetchWorkerInfo(id)

      // Wipe localStorage. workerInfo() must still return the value
      // (it goes through `infoMap` first, which is reactive and held).
      clearWorkerInfo(id)
      expect(store.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('fetchWorkerInfo short-circuits on the in-memory map without re-reading localStorage', async () => {
    // The warm path: an entry that's already in the reactive `infoMap`
    // (a sibling dialog just fetched it) must not trigger a localStorage
    // parse — the in-memory check happens first. The `clearWorkerInfo`
    // call between the two fetches pins this: if the code re-reads
    // localStorage, the second fetch would fall through to the RPC.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      await store.fetchWorkerInfo(id)
      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)

      // Wipe localStorage. The in-memory map is the only remaining
      // source of truth for this id.
      clearWorkerInfo(id)

      const info = await store.fetchWorkerInfo(id)
      expect(info?.homeDir).toBe(`/home/${id}`)
      // No second RPC — the in-memory check returned first.
      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('fetchWorkerInfo does not subscribe a reactive caller to infoMap (untrack guard)', async () => {
    // A `createEffect` that awaits `fetchWorkerInfo` would otherwise
    // pick up an implicit subscription via the in-memory check — every
    // subsequent infoMap mutation (including writes for unrelated ids)
    // would re-run the effect. The `untrack(infoMap)` read inside
    // fetchWorkerInfo prevents that.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        const other = uniqueWorkerId()
        clearWorkerInfo(id)
        clearWorkerInfo(other)
        mockGetWorkerSystemInfo
          .mockResolvedValueOnce(makeRespFor(id))
          .mockResolvedValueOnce(makeRespFor(other))

        const store = createWorkerInfoStore()
        let runs = 0
        createEffect(() => {
          runs++
          void store.fetchWorkerInfo(id)
        })
        await flush()
        // Effect ran once (mount). The first fetchWorkerInfo wrote to
        // infoMap; if that read subscribed via tracked-infoMap, the
        // effect would re-fire.
        expect(runs).toBe(1)

        // A second fetch for an unrelated id also writes to infoMap.
        await store.fetchWorkerInfo(other)
        await flush()
        // Untracked: the effect must NOT re-fire.
        expect(runs).toBe(1)
        dispose()
        done()
      })
    })
  })

  it('workerInfo() does NOT cache null reads — a sibling store writing to localStorage is visible on the next call', async () => {
    // Regression: an earlier revision cached null returns from the
    // localStorage probe in a per-store `hydrated` Map forever. Once a
    // store had observed "no cached info" for a worker id, a sibling
    // store's successful fetchWorkerInfo (which writes localStorage)
    // could not reach the original store's read — the poisoned null
    // pinned `workerInfo(id) === null` for the lifetime of the store.
    // The fix caches only positive hits; negative reads re-check
    // localStorage on every call.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        clearWorkerInfo(id)
        const storeA = createWorkerInfoStore()
        // First read sees nothing — the poisoned-null bug would cache
        // this verdict forever.
        expect(storeA.workerInfo(id)).toBeNull()

        // Sibling write to localStorage (simulating storeB.fetchWorkerInfo
        // landing while storeA was idle).
        const fresh = {
          name: 'sibling-write',
          os: 'linux' as const,
          arch: 'x64',
          homeDir: '/home/sibling',
          version: '1.0',
          commitHash: 'feed',
          buildTime: '2026-05-01',
          updatedAt: Date.now(),
        }
        setWorkerInfo(id, fresh)

        // storeA must now see the sibling-written value.
        expect(storeA.workerInfo(id)?.homeDir).toBe('/home/sibling')
        dispose()
        done()
      })
    })
  })

  it('workerInfo() reads never write to infoMap, so subscribers to unrelated entries stay quiet', async () => {
    // Localstorage-hydrated reads route through a non-reactive cache.
    // A subscriber tracking `getHomeDir(unrelated)` (an entry the read
    // never touches) must not re-fire when an unrelated worker is hydrated
    // — otherwise a workspace full of tabs reading per-id `workerInfo`
    // cascade-notifies every other consumer on initial render.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        const unrelated = uniqueWorkerId()
        setWorkerInfo(id, {
          name: 'persisted',
          os: 'linux',
          arch: 'x64',
          homeDir: '/home/persisted',
          version: '1.2',
          commitHash: 'abc',
          buildTime: '2026-02-01',
          updatedAt: Date.now(),
        })

        const store = createWorkerInfoStore()
        let unrelatedRuns = 0
        createEffect(() => {
          void store.getHomeDir(unrelated)
          unrelatedRuns++
        })
        await flush()
        expect(unrelatedRuns).toBe(1)

        // Repeated reads must never re-fire — the hydration cache is
        // non-reactive and never replaces `infoMap`'s identity.
        expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted')
        expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted')
        expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted')
        await flush()
        expect(unrelatedRuns).toBe(1)
        dispose()
        done()
      })
    })
  })
})
