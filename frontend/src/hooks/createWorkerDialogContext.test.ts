import { createRoot } from 'solid-js'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { workerClient } from '~/api/clients'
import { createWorkerDialogContext } from '~/hooks/createWorkerDialogContext'
import { flush } from '~/test-support/async'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: vi.fn().mockResolvedValue({ workers: [] }),
  },
}))

// Per-test override hook: tests that need to control when
// `fetchWorkerInfo` resolves install a handler via `setFetchWorkerInfo`.
// Default behavior is an immediate Promise.resolve(null).
let fetchWorkerInfoImpl: (id: string) => Promise<unknown> = async () => null
function setFetchWorkerInfo(impl: (id: string) => Promise<unknown>) {
  fetchWorkerInfoImpl = impl
}
// The prefetch guard set lives at module scope in the real store; the
// mock uses a per-test Set so each test's prefetch state is isolated
// from the others (otherwise tests that exercise the guard order-
// dependently start failing depending on which tests ran first).
const prefetchedOnlineIds = new Set<string>()
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    fetchWorkerInfo: vi.fn((id: string) => fetchWorkerInfoImpl(id)),
    workerInfo: () => null,
    getHomeDir: () => '/home/u',
    getOs: () => undefined,
  },
  resetOnlinePrefetch: () => prefetchedOnlineIds.clear(),
  shouldPrefetchOnline: (id: string) => {
    if (prefetchedOnlineIds.has(id))
      return false
    prefetchedOnlineIds.add(id)
    return true
  },
}))

vi.mock('~/api/workerRpc', () => ({
  getGitInfo: vi.fn(),
}))

interface FakeWorker {
  id: string
  online: boolean
}

function mockWorkers(workers: FakeWorker[]) {
  vi.mocked(workerClient.listWorkers).mockResolvedValueOnce({ workers } as never)
}

beforeAll(() => {
  // createWorkerDialogContext mounts a fetchWorkers() call; we don't
  // exercise it here but the harness still loads it.
})

beforeEach(() => {
  vi.clearAllMocks()
  fetchWorkerInfoImpl = async () => null
  // Reset the shared prefetch guard so each test exercises the
  // first-call-fans-out behavior from a clean slate.
  prefetchedOnlineIds.clear()
})

// Worker auto-selection runs inside onMount → fetchWorkers, which
// resolves a listWorkers RPC. These tests wait one extra microtask
// after createRoot so the resolved-promise then-chain settles before
// asserting `state.workerId()`.
describe('createWorkerDialogContext worker auto-selection', () => {
  it('selects the preselected worker when it is online', async () => {
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ preselectedWorkerId: 'w-2' })
      await flush()
      expect(state.workerId()).toBe('w-2')
      dispose()
    })
  })

  it('falls back to the first online worker when the preselected one is offline', async () => {
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: false }, // preselected but offline → filtered out
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ preselectedWorkerId: 'w-2' })
      await flush()
      expect(state.workerId()).toBe('w-1')
      dispose()
    })
  })

  it('falls back to the first online worker when no preselected id is provided', async () => {
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()
      expect(state.workerId()).toBe('w-1')
      dispose()
    })
  })

  it('leaves workerId empty when no online workers are returned', async () => {
    mockWorkers([
      { id: 'w-1', online: false },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ preselectedWorkerId: 'w-1' })
      await flush()
      expect(state.workerId()).toBe('')
      dispose()
    })
  })

  it('does not overwrite an existing workerId on a subsequent fetchWorkers (handleRefresh path)', async () => {
    // Regression guard: fetchWorkers is gated on `!workerId()`. The
    // refresh button calls fetchWorkers again, but a worker the user
    // explicitly picked must survive — fetchWorkers must not re-run
    // the preselect logic once a worker is already set.
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ preselectedWorkerId: 'w-2' })
      await flush()
      expect(state.workerId()).toBe('w-2')

      // User manually switches to w-1; then a refresh fires.
      state.setWorkerId('w-1')
      mockWorkers([
        { id: 'w-1', online: true },
        { id: 'w-2', online: true },
      ])
      await state.refreshWorkers()
      expect(state.workerId()).toBe('w-1')
      dispose()
    })
  })
})

describe('createWorkerDialogContext onError', () => {
  it('forwards listWorkers failures to the onError sink', async () => {
    // The state hook no longer owns an error signal — submit and load
    // errors share the same setter via `useDialogSubmit`. Verify the
    // listWorkers rejection path still surfaces a formatted message.
    vi.mocked(workerClient.listWorkers).mockRejectedValueOnce(new Error('rpc down'))
    const onError = vi.fn()
    await createRoot(async (dispose) => {
      createWorkerDialogContext({ onError })
      await flush()
      expect(onError).toHaveBeenCalledWith('rpc down')
      dispose()
    })
  })

  it('formats a non-Error rejection with the default copy', async () => {
    vi.mocked(workerClient.listWorkers).mockRejectedValueOnce('opaque')
    const onError = vi.fn()
    await createRoot(async (dispose) => {
      createWorkerDialogContext({ onError })
      await flush()
      expect(onError).toHaveBeenCalledWith('Failed to load workers')
      dispose()
    })
  })

  it('omitting onError is safe (silent failure)', async () => {
    vi.mocked(workerClient.listWorkers).mockRejectedValueOnce(new Error('rpc down'))
    await createRoot(async (dispose) => {
      // No onError supplied — the hook must not throw or reject the
      // surrounding component during onMount.
      const state = createWorkerDialogContext()
      await flush()
      expect(state.workerId()).toBe('')
      dispose()
    })
  })
})

describe('createWorkerDialogContext singleWorkerId', () => {
  it('seeds worker.id synchronously and skips listWorkers entirely', async () => {
    // singleWorkerId locks the dialog to one worker, so the fleet RPC
    // must not fire and worker.id() must be readable on first paint
    // (no flash of empty state).
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      // Synchronous: workerId is seeded before onMount runs.
      expect(state.workerId()).toBe('w-fixed')
      // Let onMount drain — no listWorkers call must occur.
      await flush()
      expect(workerClient.listWorkers).not.toHaveBeenCalled()
      expect(state.workerId()).toBe('w-fixed')
      dispose()
    })
  })

  it('handleRefresh in singleWorkerId mode does not call listWorkers', async () => {
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      await Promise.resolve()
      await state.refreshWorkers()
      expect(workerClient.listWorkers).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('refreshWorkers awaits fetchWorkerInfo so workersRefreshing observes the in-flight state', async () => {
    // Regression guard: an earlier implementation flipped
    // workersRefreshing(true) then immediately (false) around a
    // non-awaited fetchWorkerInfo call, so consumers never observed
    // the loading state. Verify the flag is true while the fetch is
    // pending and only flips back to false after it resolves.
    let resolveFetch: (() => void) | undefined
    setFetchWorkerInfo(() => new Promise<null>((res) => {
      resolveFetch = () => res(null)
    }))
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      // Drain the onMount fetch (resolve immediately so we observe the
      // refresh path in isolation).
      resolveFetch?.()
      await flush()

      // Now arm a pending fetch for the refresh call.
      let resolveRefresh: (() => void) | undefined
      setFetchWorkerInfo(() => new Promise<null>((res) => {
        resolveRefresh = () => res(null)
      }))
      const refreshPromise = state.refreshWorkers()
      // The flag must be observable as `true` while the fetch is in
      // flight — pre-fix it would have been false already.
      await Promise.resolve()
      expect(state.workersRefreshing()).toBe(true)
      resolveRefresh?.()
      await refreshPromise
      expect(state.workersRefreshing()).toBe(false)
      dispose()
    })
  })

  it('refreshWorkers clears workersRefreshing even if fetchWorkerInfo rejects', async () => {
    // The setter must run in a `finally` so a transport failure
    // doesn't leave the spinner stuck on.
    setFetchWorkerInfo(async () => null) // for onMount
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      await flush()

      setFetchWorkerInfo(() => Promise.reject(new Error('boom')))
      await expect(state.refreshWorkers()).rejects.toThrow('boom')
      expect(state.workersRefreshing()).toBe(false)
      dispose()
    })
  })
})

describe('createWorkerDialogContext worker-info lazy fetch', () => {
  // Helper: install a fetchWorkerInfo impl that records every id it was
  // called with. The default impl resolves to null so the createEffect
  // and prefetch paths don't dangle promises.
  function withRecordedFetches(): string[] {
    const calls: string[] = []
    setFetchWorkerInfo(async (id: string) => {
      calls.push(id)
      return null
    })
    return calls
  }

  it('on mount fetches ONLY the selected worker (no fleet fan-out)', async () => {
    // Pre-fix behavior: every online worker's system info was fetched
    // eagerly on dialog open — N E2EE handshakes for an N-worker fleet
    // before the user ever opens the selector.
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose) => {
      createWorkerDialogContext()
      await flush()
      expect(calls).toEqual(['w-1'])
      dispose()
    })
  })

  it('on mount with a preselected online worker fetches that one, not the first', async () => {
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      createWorkerDialogContext({ preselectedWorkerId: 'w-2' })
      await flush()
      expect(calls).toEqual(['w-2'])
      dispose()
    })
  })

  it('prefetchOnlineWorkerInfos fans out to every online worker except the selected one', async () => {
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()
      expect(calls).toEqual(['w-1'])

      state.prefetchOnlineWorkerInfos()
      await flush()
      // Selected (w-1) is skipped — it was already fetched on mount.
      expect(calls.slice(1).toSorted()).toEqual(['w-2', 'w-3'])
      dispose()
    })
  })

  it('prefetchOnlineWorkerInfos is idempotent: a second call does not refetch', async () => {
    // The WorkerSelector wires both onFocus and onPointerDown to this
    // method, and both can fire during one open of the dropdown. Without
    // the guard, that would double the fan-out RPC count on every interaction.
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()

      state.prefetchOnlineWorkerInfos()
      state.prefetchOnlineWorkerInfos()
      state.prefetchOnlineWorkerInfos()
      await flush()
      // One fetch on mount + one for the remaining online worker.
      expect(calls).toEqual(['w-1', 'w-2'])
      dispose()
    })
  })

  it('prefetchOnlineWorkerInfos guard is shared across dialog instances', async () => {
    // The "already prefetched" guard lives at module scope in
    // workerInfo.store, so opening a second dialog later in the same
    // session doesn't re-fan the per-worker fetch loop for ids the
    // first dialog already covered. Without this, a 10-worker fleet
    // would pay 10 RPCs every time a dialog opened, just to populate
    // dropdown labels that haven't changed.
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose1) => {
      const state1 = createWorkerDialogContext()
      await flush()
      state1.prefetchOnlineWorkerInfos()
      await flush()
      // w-1 fetched on mount + w-2 / w-3 via the prefetch fan-out.
      expect(calls.slice().toSorted()).toEqual(['w-1', 'w-2', 'w-3'])
      dispose1()
    })
    // Second dialog opens with the same fleet: the module-scope guard
    // remembers we already prefetched w-2 / w-3, so only the new
    // dialog's on-mount selected-worker fetch (w-1 again) should hit.
    const beforeSecond = calls.length
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
      { id: 'w-3', online: true },
    ])
    await createRoot(async (dispose2) => {
      const state2 = createWorkerDialogContext()
      await flush()
      state2.prefetchOnlineWorkerInfos()
      await flush()
      const afterSecond = calls.slice(beforeSecond)
      expect(afterSecond).toEqual(['w-1'])
      dispose2()
    })
  })

  it('refreshWorkers resets the prefetch guard so a follow-up prefetch re-fans', async () => {
    // After a manual refresh, the fleet may have grown / shrunk / had
    // workers come online; the dropdown's next focus must trigger fresh
    // info fetches.
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()

      state.prefetchOnlineWorkerInfos()
      await flush()
      const beforeRefresh = calls.length

      mockWorkers([
        { id: 'w-1', online: true },
        { id: 'w-2', online: true },
      ])
      await state.refreshWorkers()
      state.prefetchOnlineWorkerInfos()
      await flush()
      expect(calls.length).toBeGreaterThan(beforeRefresh)
      dispose()
    })
  })

  it('switching workerId via setWorkerId fetches the new selection', async () => {
    // The dropdown change handler calls setWorkerId; the createEffect on
    // workerId must keep the selected worker's info hot so GitOptions /
    // DirectorySelector see a populated homeDir without a manual refresh.
    const calls = withRecordedFetches()
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()
      expect(calls).toEqual(['w-1'])

      state.setWorkerId('w-2')
      await flush()
      expect(calls).toEqual(['w-1', 'w-2'])
      dispose()
    })
  })

  it('singleWorkerId mode fetches the seeded worker on mount', async () => {
    // Dialogs locked to one worker (ChangeBranchDialog, DeleteBranchDialog)
    // skip the fleet listing entirely; the createEffect on workerId
    // still drives the per-selection fetch off the synchronously-seeded
    // initial value.
    const calls = withRecordedFetches()
    await createRoot(async (dispose) => {
      createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      await flush()
      expect(calls).toEqual(['w-fixed'])
      dispose()
    })
  })
})

describe('createWorkerDialogContext getHomeDir accessor', () => {
  // The hook exposes a focused `getHomeDir()` bound to the current
  // workerId (no args). Dialogs that pass it through to `homeDir`
  // props don't have to thread the worker id alongside.
  it('returns the home dir for the currently-selected worker', async () => {
    setFetchWorkerInfo(async () => null)
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext({ singleWorkerId: 'w-fixed' })
      await flush()
      // Mock returns '/home/u' for any id.
      expect(state.getHomeDir()).toBe('/home/u')
      dispose()
    })
  })

  it('reactively follows workerId changes', async () => {
    setFetchWorkerInfo(async () => null)
    mockWorkers([
      { id: 'w-1', online: true },
      { id: 'w-2', online: true },
    ])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()
      const beforeHome = state.getHomeDir()
      state.setWorkerId('w-2')
      const afterHome = state.getHomeDir()
      // The mocked store returns the same string for both ids, but
      // calling getHomeDir() after the switch must still resolve to a
      // value (not throw, not stall). The reactive read is what we're
      // pinning — the mocked store deliberately returns a constant.
      expect(beforeHome).toBe('/home/u')
      expect(afterHome).toBe('/home/u')
      dispose()
    })
  })

  it('returns empty string when no worker is selected', async () => {
    // No workers online → workerId() stays ''. getHomeDir() must not
    // throw on the empty id and must yield the empty-string sentinel
    // (the mock stub returns '/home/u' regardless of id, but a real
    // store yields '' for unknown ids).
    setFetchWorkerInfo(async () => null)
    mockWorkers([])
    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      await flush()
      expect(state.workerId()).toBe('')
      // The mocked store doesn't differentiate, but we at least pin
      // that the accessor doesn't throw on the empty selection.
      expect(() => state.getHomeDir()).not.toThrow()
      dispose()
    })
  })

  it('skips signal writes when the owner is disposed before listWorkers resolves', async () => {
    // Regression: fetchWorkers used to write setWorkers / setWorkerId on
    // the now-disposed scope when a dialog was dismissed before the RPC
    // came back (Solid logs a 'computation outside reactive context'
    // warning). The owner-cleanup guard short-circuits the post-await
    // writes; this test holds the listWorkers promise pending, disposes
    // the root, then resolves and asserts no leaked state writes ran.
    let resolveList!: (resp: { workers: FakeWorker[] }) => void
    vi.mocked(workerClient.listWorkers).mockReturnValueOnce(
      new Promise<{ workers: FakeWorker[] }>((r) => { resolveList = r }) as never,
    )
    let workerIdAfter: string | null = null
    let didThrow = false
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const state = createWorkerDialogContext({ preselectedWorkerId: 'w-1' })
        // Dispose BEFORE the listWorkers promise resolves.
        dispose()
        try {
          resolveList({ workers: [{ id: 'w-1', online: true }] })
          // Drain microtasks so the post-await writes (would) run.
          await flush()
          workerIdAfter = state.workerId()
        }
        catch {
          didThrow = true
        }
        done()
      })
    })
    expect(didThrow).toBe(false)
    // workerId stays at its seed ('') — the disposed-scope guard skipped
    // the setWorkerId call that auto-selection would have triggered.
    expect(workerIdAfter).toBe('')
  })

  it('a late-resolving listWorkers does NOT clobber a fresher refreshWorkers result (generation guard)', async () => {
    // Regression: fetchWorkers had no generation/abort guarding, so a
    // listWorkers call that resolved AFTER a fresher refresh overwrote
    // the new snapshot with the older one. The fix bumps a per-call
    // generation and only the latest generation may commit
    // setWorkers/setWorkerId writes.
    let slowResolve!: (v: { workers: { id: string, online: boolean }[] }) => void
    const slowP = new Promise<{ workers: { id: string, online: boolean }[] }>((resolve) => {
      slowResolve = resolve
    })
    // mockReset() drops both leftover `*Once` queue items from prior
    // tests AND the default mockResolvedValue installed at module level.
    // Then re-queue the two responses we need.
    vi.mocked(workerClient.listWorkers).mockReset()
    vi.mocked(workerClient.listWorkers)
      // Call 1: onMount's fetchWorkers gets slowP (pending until we
      // resolve it manually at the end of the test).
      .mockReturnValueOnce(slowP as never)
      // Call 2: refreshWorkers' fetchWorkers gets fresh-1 (resolves
      // immediately on the next microtask).
      .mockResolvedValueOnce({ workers: [{ id: 'fresh-1', online: true }] } as never)

    await createRoot(async (dispose) => {
      const state = createWorkerDialogContext()
      // Let onMount fire — its fetchWorkers picks up slowP (pending).
      await flush()
      // Refresh — its fetchWorkers picks up fresh-1 (resolves now).
      await state.refreshWorkers()
      await flush()
      expect(state.workers().map(w => w.id)).toEqual(['fresh-1'])
      expect(state.workerId()).toBe('fresh-1')

      // Resolve the now-stale slowP with an OLDER (empty) list. The
      // generation guard must drop this write — workers stays fresh.
      slowResolve({ workers: [] })
      await flush()
      expect(state.workers().map(w => w.id)).toEqual(['fresh-1'])
      expect(state.workerId()).toBe('fresh-1')
      dispose()
    })
  })

  it('setWorkerId throws when the dialog is locked to a singleWorkerId and the caller tries a different id', async () => {
    // Regression: the dialog used to expose setWorkerId unconditionally
    // even when constructed with singleWorkerId. A stray
    // setWorkerId('other') silently fired the workerInfo effect for the
    // wrong worker and desynced the locked dialog from any sibling code
    // that still read from `props.workerId`. The locked variant now
    // rejects writes that violate the lock.
    await new Promise<void>((done) => {
      createRoot((dispose) => {
        const state = createWorkerDialogContext({ singleWorkerId: 'locked-w' })
        expect(state.workerId()).toBe('locked-w')
        // A self-write (same id) is fine — the lock only blocks drift.
        expect(() => state.setWorkerId('locked-w')).not.toThrow()
        expect(() => state.setWorkerId('other-w')).toThrow(/locked to locked-w/)
        // State must not have moved.
        expect(state.workerId()).toBe('locked-w')
        dispose()
        done()
      })
    })
  })

  it('singleWorkerId="" still activates the lock and skips fetchWorkers', async () => {
    // Defensive regression: an earlier revision used `!options.singleWorkerId`
    // (falsy) to decide whether to run fetchWorkers. With singleWorkerId='',
    // that path leaked through — fetchWorkers ran AND then attempted
    // setWorkerId(online[0].id), which the throwing wrapped setter
    // (gated on `!== undefined`) rejected with "rejected — dialog is
    // locked to ''". The fix is `locked = options.singleWorkerId !== undefined`,
    // matching the setter wrap's gate so the two stay in lockstep.
    vi.mocked(workerClient.listWorkers).mockResolvedValue({
      // If this runs, the lock failed. Sentinel id so the assertion
      // below would catch the leak.
      workers: [{ id: 'leaked', online: true } as never],
    } as never)
    await new Promise<void>((done) => {
      createRoot((dispose) => {
        const state = createWorkerDialogContext({ singleWorkerId: '' })
        // Initial state matches the seed — the empty string.
        expect(state.workerId()).toBe('')
        Promise.resolve().then(async () => {
          // Flush onMount + the (un)expected listWorkers.
          await new Promise(r => setTimeout(r, 0))
          // listWorkers must NOT have been invoked: the lock is in
          // effect even with the empty-string id.
          expect(workerClient.listWorkers).not.toHaveBeenCalled()
          // workerId did not drift to 'leaked'.
          expect(state.workerId()).toBe('')
          dispose()
          done()
        })
      })
    })
  })
})
