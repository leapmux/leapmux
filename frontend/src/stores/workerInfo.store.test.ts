import type { WorkerInfo } from '~/lib/workerInfoCache'
import { createEffect, createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerProjectionsEqual } from '~/components/workspace/WorkspaceTabTree'
import { deferred, flush } from '~/test-support/async'
import { useTestStorage } from '~/test-support/persistentStorage'

// Worker info is on the ASYNCHRONOUS storage tier -- one row per worker the
// user has ever reached, so the family is unbounded and deliberately not
// mirrored in memory. These round-trips therefore need a database.
useTestStorage()

const mockGetWorkerSystemInfo = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getWorkerSystemInfo: (...args: unknown[]) => mockGetWorkerSystemInfo(...args),
}))

// Imported after the mock so the store closure binds to the mocked RPC.
const { createWorkerInfoStore, workerInfoStore, resetOnlinePrefetch } = await import('~/stores/workerInfo.store')
const workerInfoCache = await import('~/lib/workerInfoCache')
const { getWorkerInfo, setWorkerInfo, clearWorkerInfo } = workerInfoCache

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

// Each test uses a fresh workerId so the module-scope sharedPending / stored
// rows don't carry state across runs.
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
  it('issues the RPC when nothing is stored and caches the result', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)
      expect(info?.homeDir).toBe(`/home/${id}`)
      // Reactive accessor reflects the fetched info.
      expect(store.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      // The stored row is written for cross-store sharing.
      expect((await getWorkerInfo(id))?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('skips the RPC when the stored row is within the freshness TTL', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      // Seed a fresh snapshot straight into storage.
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
      // Reactive map is hydrated from the stored row so downstream reads
      // pick up the cached value without an extra dance.
      expect(store.workerInfo(id)?.homeDir).toBe('/home/cached')
      dispose()
    })
  })

  it('issues the RPC when the stored row is past the freshness TTL', async () => {
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
      // The stored row is overwritten so the next dialog within the
      // freshness window will see the refreshed payload.
      expect((await getWorkerInfo(id))?.homeDir).toBe('/home/fresh')
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
      // Polled, because each fetch first reads the persisted cache to decide
      // whether the round trip is needed at all.
      await vi.waitFor(() => expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1))

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

  it('returns null on RPC failure and does not poison the stored cache', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockRejectedValueOnce(new Error('worker offline'))

      const store = createWorkerInfoStore()
      const info = await store.fetchWorkerInfo(id)

      expect(info).toBeNull()
      // No write on failure — a future fetch attempt must
      // still hit the RPC instead of falling for a stale "success".
      expect((await getWorkerInfo(id))).toBeNull()
      dispose()
    })
  })

  it('workerInfo() hydrates a fresh store from the stored row on first access', async () => {
    // Across a page reload (or a new dialog opening with a fresh store),
    // the reactive infoMap starts empty but storage already holds
    // the prior snapshot — workerInfo() must surface it transparently
    // (via its non-reactive hydration cache).
    await createRoot(async (dispose) => {
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
      // `workerInfo` stays SYNCHRONOUS for the reactive readers that call it,
      // so a cold store answers null and starts the read; the row arrives
      // through `infoMap`, which is what re-renders the readers.
      expect(store.workerInfo(id)).toBeNull()
      await vi.waitFor(() => expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted'))
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
      await vi.waitFor(() => expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1))

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

  it('fetchWorkerInfo() still notifies subscribers after a workerInfo() read hydrated from storage', async () => {
    // Regression guard for the non-reactive hydration cache. workerInfo()
    // now routes stored hits through a non-reactive Map, so a
    // careless refactor could end up shadowing future writes (subscriber
    // reads stale cached value forever). This test pins that:
    //   1. The initial workerInfo() read returns the stored value.
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
        // TWO observations, and the first is the cold answer. `workerInfo` is
        // synchronous, so the effect runs once before the persisted row has
        // been read and once after this worker's revision signal announces it
        // -- which is precisely the reactive notification this test exists to
        // prove is still live.
        await vi.waitFor(() => expect(observed).toEqual([undefined, '/home/stale']))

        mockGetWorkerSystemInfo.mockResolvedValueOnce(
          makeRespFor(id, { homeDir: '/home/fresh' }),
        )
        await store.fetchWorkerInfo(id)
        await flush()

        // Subscriber observed the post-fetch value: the non-reactive
        // hydration cache did not block the reactive `infoMap` write.
        expect(observed).toEqual([undefined, '/home/stale', '/home/fresh'])
        dispose()
        done()
      })
    })
  })

  it('fetchWorkerInfo populates the hydrated cache so a later workerInfo() read survives a storage wipe', async () => {
    // The `hydrated` cache is shared between workerInfo() and
    // fetchWorkerInfo. A successful fetch must seed the cache so a
    // subsequent workerInfo() — say, after a sweep wiped the row
    // — still surfaces the prior result instead of re-hitting
    // storage and observing the gap. Pairs with the in-memory
    // short-circuit test below, which covers the reactive infoMap;
    // this one specifically covers the non-reactive hydration cache.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      await store.fetchWorkerInfo(id)

      // Wipe the stored row. workerInfo() must still return the value
      // (it goes through `infoMap` first, which is reactive and held).
      clearWorkerInfo(id)
      expect(store.workerInfo(id)?.homeDir).toBe(`/home/${id}`)
      dispose()
    })
  })

  it('fetchWorkerInfo short-circuits on the in-memory map without re-reading storage', async () => {
    // The warm path: an entry that's already in the reactive `infoMap`
    // (a sibling dialog just fetched it) must not trigger a stored read
    // — the in-memory check happens first. The `clearWorkerInfo`
    // call between the two fetches pins this: if the code re-reads
    // storage, the second fetch would fall through to the RPC.
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      clearWorkerInfo(id)
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id))

      const store = createWorkerInfoStore()
      await store.fetchWorkerInfo(id)
      expect(mockGetWorkerSystemInfo).toHaveBeenCalledTimes(1)

      // Wipe the stored row. The in-memory map is the only remaining
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

  it('workerInfo() does NOT cache null reads — a sibling store writing to storage is visible on the next call', async () => {
    // Regression: an earlier revision cached null returns from the
    // stored probe in a per-store `hydrated` Map forever. Once a
    // store had observed "no cached info" for a worker id, a sibling
    // store's successful fetchWorkerInfo (which writes the row)
    // could not reach the original store's read — the poisoned null
    // pinned `workerInfo(id) === null` for the lifetime of the store.
    // The fix caches only positive hits; negative reads re-check
    // storage on every call.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        clearWorkerInfo(id)
        const storeA = createWorkerInfoStore()
        // First read sees nothing — the poisoned-null bug would cache
        // this verdict forever.
        expect(storeA.workerInfo(id)).toBeNull()

        // Sibling write to storage (simulating storeB.fetchWorkerInfo
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

        // storeA must now see the sibling-written value. Polled, because the
        // re-read is asynchronous; the invariant is that it happens AT ALL --
        // a cached null would pin `workerInfo(id)` at null forever.
        await vi.waitFor(() => expect(storeA.workerInfo(id)?.homeDir).toBe('/home/sibling'))
        dispose()
        done()
      })
    })
  })

  // THE LATE-FILL GUARD. `workerInfo()` starts a persisted read and answers
  // null for now, so a fetch can land while that read is still in flight -- and
  // the fetch is newer than anything on disk BY CONSTRUCTION. The stored row
  // must lose.
  //
  // What this pins is the PUBLISHING CHANNEL, which is where the mistake is
  // easy to make: a late read that published through `infoMap` -- the obvious
  // reading of "so the rows re-render" -- would replace the fetched value with
  // the older stored one. It publishes through the worker's own revision signal
  // instead, and `workerInfo` reads `infoMap` ahead of it.
  //
  // The read is held open by hand. Left to real timing the fetch is the slower
  // of the two -- it reads the same row before it decides to spend an RPC -- so
  // this ordering would never occur here.
  it('drops a persisted read that lands after a fresher fetch', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const id = uniqueWorkerId()
        const held = deferred<WorkerInfo | null>()
        const read = vi.spyOn(workerInfoCache, 'getWorkerInfo').mockReturnValueOnce(held.promise)

        const store = createWorkerInfoStore()
        const observed: (string | null | undefined)[] = []
        createEffect(() => observed.push(store.workerInfo(id)?.homeDir ?? null))
        await flush()
        expect(observed).toEqual([null])

        // The fetch overtakes the held read. Only this call reaches the real
        // reader, which is what `mockReturnValueOnce` leaves behind.
        read.mockRestore()
        mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id, { homeDir: '/home/fresh' }))
        await store.fetchWorkerInfo(id)
        await flush()
        expect(observed).toEqual([null, '/home/fresh'])

        // The read finally answers, with what was on disk BEFORE the fetch.
        held.resolve({
          name: 'stale-on-disk',
          os: 'linux',
          arch: 'x64',
          homeDir: '/home/stale',
          version: '1.0',
          commitHash: 'old',
          buildTime: '2025-01-01',
          updatedAt: Date.now() - 5 * 60 * 1000,
        })
        await flush()
        await flush()

        // Unchanged, and NOT re-notified: a third entry here would be a
        // re-render of every row showing this worker, for the same value.
        expect(observed).toEqual([null, '/home/fresh'])
        expect(store.workerInfo(id)?.homeDir).toBe('/home/fresh')
        dispose()
        done()
      })
    })
  })

  it('workerInfo() reads never write to infoMap, so subscribers to unrelated entries stay quiet', async () => {
    // Storage-hydrated reads route through a non-reactive cache.
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

        // Hydrating THIS worker must not re-fire the unrelated subscriber, and
        // repeated reads must not either: the persisted row lands in a
        // non-reactive cache plus a revision signal scoped to its own id, never
        // in `infoMap`, whose identity every consumer shares.
        await vi.waitFor(() => expect(store.workerInfo(id)?.homeDir).toBe('/home/persisted'))
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

// THE CONTRACT `WorkspaceTabTree` RESTS ON, pinned at the store rather than at
// the component that consumes it.
//
// `workersProjection` builds `{ id, info: workerInfo(id) }` per worker and gates
// its own recompute on `workerProjectionsEqual`. So a worker's info changing
// must make the OLD and NEW reads compare UNEQUAL -- otherwise the memo keeps
// its previous value, `buildTree` never re-runs, and the tree shows the stale
// record. That is the exact freeze the memo's comment records fixing: every row
// reads `homeDir` to shorten its directory, and the system info arrives on its
// own RPC after the first paint.
//
// It holds today because `workerInfo` answers with a plain record and each write
// installs a new one. It would STOP holding the moment the store handed out a
// live reference into its own state -- both sides of the comparison would then
// be the same object, every field would compare equal, and the tree would freeze
// with nothing to show for it.
describe('workerInfo feeds a projection that can tell a change happened', () => {
  it('compares unequal after the worker info changes', async () => {
    await createRoot(async (dispose) => {
      const id = uniqueWorkerId()
      // ONE store, because that is what `workersProjection` holds: a single
      // `workerInfoFn` it calls before and after. Two instances would compare
      // two separate caches and could not see a shared reference at all.
      const store = createWorkerInfoStore()

      // The first reading arrives from disk, which is how a worker's info shows
      // up before any RPC. Stamped past the freshness window, so the fetch below
      // actually spends its round trip instead of answering from the row.
      setWorkerInfo(id, { ...makeRespFor(id, { homeDir: '/home/before' }), updatedAt: Date.now() - 10 * 60_000 })
      expect(store.workerInfo(id)).toBeNull()
      await flush()
      const before = store.workerInfo(id)
      expect(before?.homeDir).toBe('/home/before')

      // The system-info RPC then lands with a different home directory, which is
      // the case the tree froze on: every row shortens its directory with it.
      mockGetWorkerSystemInfo.mockResolvedValueOnce(makeRespFor(id, { homeDir: '/home/after' }))
      const after = await store.fetchWorkerInfo(id)
      expect(after?.homeDir).toBe('/home/after')

      expect(
        workerProjectionsEqual([{ id, info: before }], [{ id, info: after }]),
        'the projection must see the change, or the tab tree freezes on the stale record',
      ).toBe(false)
      dispose()
    })
  })
})
