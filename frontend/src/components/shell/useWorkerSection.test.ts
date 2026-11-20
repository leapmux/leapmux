import type { KeyPinConfirmState } from './AppShellDialogs'
import type { TunnelInfo } from '~/api/platformBridge'
/// <reference types="vitest/globals" />
import type { WorkerTrustScope } from '~/context/AuthContext'
import type { KeyPinConfirmFn } from '~/lib/keyPinStore'
import { create } from '@bufbuild/protobuf'
import { timestampFromMs } from '@bufbuild/protobuf/wkt'
import { createComponent, createMemo, createRoot, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { createDialogState } from '~/hooks/createDialogState'
import { createTunnelStore } from '~/stores/tunnel.store'
import { deferred } from '~/test-support/async'
import { captureLoggerOutput } from '~/test-support/loggerCapture'
import { useWorkerSection } from './useWorkerSection'

const mockListWorkers = vi.fn()
const mockDeregisterWorker = vi.fn()
vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: (...a: unknown[]) => mockListWorkers(...a),
    deregisterWorker: (...a: unknown[]) => mockDeregisterWorker(...a),
  },
}))

const mockSetConfirmKeyPin = vi.fn()
const mockUnregisterKeyPin = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  channelManager: {},
  // Returns a disposer, like the real `setConfirmKeyPin`. A double that
  // returned nothing would make `onCleanup(...)` throw on dispose AND would let
  // a regression that drops the unregister pass silently.
  setConfirmKeyPin: (fn: unknown) => {
    mockSetConfirmKeyPin(fn)
    return mockUnregisterKeyPin
  },
}))

const mockFetchWorkerInfo = vi.fn()
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    workerInfo: vi.fn(),
    fetchWorkerInfo: (...a: unknown[]) => mockFetchWorkerInfo(...a),
  },
}))

vi.mock('~/stores/workerChannelStatus.store', () => ({
  createWorkerChannelStatusStore: () => ({
    getStatus: vi.fn(),
  }),
}))
const mockTunnelRows = vi.fn(() => [] as Array<{ keyMismatch?: object }>)
const mockTunnelRefresh = vi.fn()
const mockOnEvent = vi.fn()
const mockListTunnels = vi.fn()
vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    isTunnelAvailable: () => true,
    platformBridge: {
      ...actual.platformBridge,
      onEvent: (...a: unknown[]) => mockOnEvent(...a),
      listTunnels: (...a: unknown[]) => mockListTunnels(...a),
    },
  }
})
vi.mock('~/stores/tunnel.store', () => ({
  createTunnelStore: vi.fn(() => ({
    tunnels: () => mockTunnelRows(),
    tunnelsForWorker: vi.fn(() => []),
    refresh: (...a: unknown[]) => mockTunnelRefresh(...a),
    add: vi.fn(),
    remove: vi.fn(),
    removeAllForWorker: vi.fn(),
  })),
}))

function trustScope(userId: string): WorkerTrustScope {
  return {
    userId: userId || null,
    generation: 1,
    signal: new AbortController().signal,
  }
}

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

/**
 * A decoded Worker, as the hub client hands one over. A plain object would
 * not exercise the cache's comparison, which reads the message schema.
 */
function worker(id: string, online = true) {
  return create(WorkerSchema, { id, online })
}

beforeEach(() => {
  mockListWorkers.mockReset()
  mockListWorkers.mockResolvedValue({
    workers: [
      worker('w1'),
    ],
  })
  mockDeregisterWorker.mockReset()
  mockDeregisterWorker.mockResolvedValue({})
  mockFetchWorkerInfo.mockReset()
  mockSetConfirmKeyPin.mockReset()
  mockUnregisterKeyPin.mockReset()
  mockTunnelRows.mockReset()
  mockTunnelRows.mockReturnValue([])
  mockTunnelRefresh.mockReset()
  mockTunnelRefresh.mockResolvedValue(undefined)
  mockListTunnels.mockReset()
  mockOnEvent.mockReset()
  mockOnEvent.mockResolvedValue(() => {})
})

/**
 * The worker registry, lifted out of `AppShell`. It shares nothing with the
 * rest of that component — no tabs, no layout, no projection — so it is
 * testable on its own, which it was not while it lived inline.
 */
describe('useWorkerSection', () => {
  function mount(getUserId: () => string = () => 'u1') {
    const scope = createMemo(() => trustScope(getUserId()))
    return useWorkerSection({
      getUserId,
      workerTrustScope: scope,
      keyPinConfirmDialog: createDialogState(),
    })
  }

  it('fetches workers once the user id is known', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)
      expect(s.workers().map(w => w.id)).toEqual(['w1'])
      dispose()
    })
  })

  // The tunnel list is the sidecar's, and it answers only the rows one
  // identity owns — so the listing specifies that identity, and it is read as
  // soon as the identity is known.
  it('lists the tunnels for the identity', async () => {
    await createRoot(async (dispose) => {
      mount(() => 'u9')
      await flush()
      expect(mockTunnelRefresh).toHaveBeenCalledWith(expect.objectContaining({ userId: 'u9' }), 'coalesce')
      dispose()
    })
  })

  it('waits for native listener registration before reading the first snapshot', async () => {
    const registration = deferred<() => void>()
    mockOnEvent.mockReturnValue(registration.promise)
    await createRoot(async (dispose) => {
      mount()
      await flush()
      try {
        expect(mockTunnelRefresh).not.toHaveBeenCalled()
      }
      finally {
        dispose()
        registration.resolve(() => {})
      }
    })
  })

  // The native hint carries no tunnel data: re-reading the list is how the
  // page learns that a tunnel stopped on a Worker key mismatch.
  it('re-reads the tunnels when the sidecar announces a change', async () => {
    await createRoot(async (dispose) => {
      let announce: (() => void) | undefined
      mockOnEvent.mockImplementation((_event: string, handler: () => void) => {
        announce = handler
        return Promise.resolve(() => {})
      })
      mount()
      await flush()
      mockTunnelRefresh.mockClear()

      announce?.()
      expect(mockTunnelRefresh).toHaveBeenCalledWith(expect.objectContaining({ userId: 'u1' }), 'invalidate')
      dispose()
    })
  })

  it('disposes a native listener that registers after the shell is gone', async () => {
    const registration = deferred<() => void>()
    const unlisten = vi.fn()
    mockOnEvent.mockReturnValue(registration.promise)
    const dispose = createRoot((dispose) => {
      mount()
      return dispose
    })
    dispose()
    registration.resolve(unlisten)
    await flush()

    expect(unlisten).toHaveBeenCalledOnce()
    expect(mockTunnelRefresh).not.toHaveBeenCalled()
  })

  it('refreshes on return to view and polls only while visible with an active tunnel', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    mockTunnelRows.mockReturnValue([{}])
    const unlisten = vi.fn()
    mockOnEvent.mockResolvedValue(unlisten)
    const dispose = createRoot((dispose) => {
      mount()
      return dispose
    })

    try {
      await vi.advanceTimersByTimeAsync(0)
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(29_999)
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1)
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(2)

      visibility.mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(60_000)
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(2)

      visibility.mockReturnValue('visible')
      document.dispatchEvent(new Event('visibilitychange'))
      expect(mockTunnelRefresh).toHaveBeenLastCalledWith(expect.any(Object), 'invalidate')
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(3)

      mockTunnelRows.mockReturnValue([
        { keyMismatch: {} },
      ])
      visibility.mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      visibility.mockReturnValue('visible')
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(60_000)
      expect(mockTunnelRefresh).toHaveBeenCalledTimes(4)
    }
    finally {
      dispose()
      visibility.mockRestore()
      vi.useRealTimers()
    }
    expect(unlisten).toHaveBeenCalledOnce()
  })

  it('publishes stopped status after a poll when a native hint is lost', async () => {
    const actual = await vi.importActual<typeof import('~/stores/tunnel.store')>('~/stores/tunnel.store')
    vi.mocked(createTunnelStore).mockImplementationOnce(actual.createTunnelStore)
    const live: TunnelInfo = {
      id: 'tunnel',
      workerId: 'w1',
      type: 'socks5',
      bindAddr: '127.0.0.1',
      bindPort: 1080,
      targetAddr: '',
      targetPort: 0,
    }
    const stopped = {
      ...live,
      keyMismatch: { expectedFingerprint: 'expected', presentedFingerprint: 'presented' },
    }
    mockListTunnels
      .mockResolvedValueOnce({
        tunnels: [live],
        channelEpoch: 'epoch',
      })
      .mockResolvedValueOnce({
        tunnels: [stopped],
        channelEpoch: 'epoch',
      })
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    try {
      await createRoot(async (dispose) => {
        try {
          const section = mount()
          await vi.advanceTimersByTimeAsync(0)
          expect(section.tunnelStore.tunnels()).toEqual([live])

          await vi.advanceTimersByTimeAsync(30_000)
          expect(mockListTunnels).toHaveBeenCalledTimes(2)
          expect(section.tunnelStore.tunnels()).toEqual([stopped])
          await vi.advanceTimersByTimeAsync(60_000)
          expect(mockListTunnels).toHaveBeenCalledTimes(2)
        }
        finally {
          dispose()
        }
      })
    }
    finally {
      visibility.mockRestore()
      vi.useRealTimers()
    }
  })

  // The hub answers one page per call. A sidebar that read only the first page
  // hid every worker past it, with no sign that more existed.
  it('lists every worker across pages', async () => {
    await createRoot(async (dispose) => {
      mockListWorkers.mockImplementation(async (req: { page?: { cursor?: string } }) => (
        req.page?.cursor === 'next'
          ? {
              workers: [
                worker('w2'),
              ],
              page: { nextCursor: '' },
            }
          : {
              workers: [
                worker('w1'),
              ],
              page: { nextCursor: 'next' },
            }
      ))
      const s = mount()
      await flush()
      await flush()
      expect(s.workers().map(w => w.id)).toEqual(['w1', 'w2'])
      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  it('does not fetch before the session restores a user', async () => {
    await createRoot(async (dispose) => {
      mount(() => '')
      await flush()
      // A blank user id means the session has not restored yet; listing then
      // would be an unauthenticated round-trip on every cold load.
      expect(mockListWorkers).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('keeps worker object identity stable across refetches', async () => {
    await createRoot(async (dispose) => {
      // listWorkers deserializes FRESH objects every call, so the mock must
      // too -- `mockResolvedValue` hands back one shared object, which would
      // preserve identity on its own and make this assertion vacuous.
      mockListWorkers.mockImplementation(async () => ({
        workers: [
          worker('w1'),
        ],
      }))
      const s = mount()
      await flush()
      const first = s.workers()[0]

      s.refetchWorkers()
      await flush()

      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      expect(s.workers()[0], 'same reference, so the sidebar row is not re-created').toBe(first)
      dispose()
    })
  })

  // A decoded Worker carries its created and last-seen times as nested
  // Timestamp messages, fresh on every reply. The sidebar keys its rows by
  // object, so a cache that compared those by reference re-created every
  // worker row on every refetch, and an open row menu closed with it.
  it('keeps the object of a worker whose refetch carries the same times', async () => {
    await createRoot(async (dispose) => {
      mockListWorkers.mockImplementation(async () => ({
        workers: [
          create(WorkerSchema, {
            id: 'w1',
            online: true,
            createdAt: timestampFromMs(1_700_000_000_000),
            lastSeenAt: timestampFromMs(1_700_000_500_000),
          }),
        ],
      }))
      const s = mount()
      await flush()
      const first = s.workers()[0]
      expect(first?.createdAt, 'the reply carries a created time').toBeDefined()

      s.refetchWorkers()
      await flush()

      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      expect(s.workers()[0]).toBe(first)
      dispose()
    })
  })

  it('replaces the object of a worker whose last-seen time changed', async () => {
    await createRoot(async (dispose) => {
      const lastSeen = { ms: 1_700_000_500_000 }
      mockListWorkers.mockImplementation(async () => ({
        workers: [
          create(WorkerSchema, {
            id: 'w1',
            online: true,
            createdAt: timestampFromMs(1_700_000_000_000),
            lastSeenAt: timestampFromMs(lastSeen.ms),
          }),
        ],
      }))
      const s = mount()
      await flush()
      const first = s.workers()[0]

      lastSeen.ms += 60_000
      s.refetchWorkers()
      await flush()

      expect(s.workers()[0]).not.toBe(first)
      expect(s.workers()[0]?.lastSeenAt?.seconds).toBe(1_700_000_560n)
      dispose()
    })
  })

  it('coalesces refetches requested during a fetch into exactly one more fetch', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)

      let release!: () => void
      mockListWorkers.mockImplementationOnce(() => new Promise((resolve) => {
        release = () => resolve({
          workers: [
            worker('w1'),
            worker('w2'),
          ],
        })
      }))
      s.refetchWorkers()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(2)

      // Three events arrive while that fetch is in flight.
      s.refetchWorkers()
      s.refetchWorkers()
      s.refetchWorkers()
      expect(mockListWorkers, 'nothing starts while a fetch runs').toHaveBeenCalledTimes(2)

      release()
      await flush()
      await flush()
      expect(mockListWorkers, 'one more fetch covers all three events').toHaveBeenCalledTimes(3)
      expect(s.workers().map(w => w.id)).toEqual(['w1'])
      dispose()
    })
  })

  it('starts a new fetch for a refetch requested after the previous one settled', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      s.refetchWorkers()
      await flush()
      s.refetchWorkers()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(3)
      dispose()
    })
  })

  it('keeps refetching after a failed fetch', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      mockListWorkers.mockRejectedValueOnce(new Error('offline'))
      s.refetchWorkers()
      await flush()
      s.refetchWorkers()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(3)
      expect(s.workers().map(w => w.id)).toEqual(['w1'])
      dispose()
    })
  })

  it('warms worker info only for ONLINE workers', async () => {
    await createRoot(async (dispose) => {
      mockListWorkers.mockResolvedValue({
        workers: [
          worker('up', true),
          worker('down', false),
        ],
      })
      mount()
      await flush()

      expect(mockFetchWorkerInfo).toHaveBeenCalledTimes(1)
      expect(mockFetchWorkerInfo).toHaveBeenCalledWith('up')
      dispose()
    })
  })

  // The count alone proves nothing -- the hook calls `setConfirmKeyPin` once,
  // unconditionally, in its body, so `toHaveBeenCalledTimes(1)` after a single
  // mount holds for every implementation that registers at all. What has to be
  // pinned is the DISPOSE half: the registration outlives the mount otherwise,
  // and a prompt closed over a dead mount's dialog never settles --
  // `KeyPinStore` awaits it with no timeout and queues every later prompt
  // behind it, so one TOFU mismatch in that window deadlocks key-pinning for
  // the whole page.
  it('registers the key-pin prompt for the live mount and drops it on dispose', async () => {
    await createRoot(async (dispose) => {
      mount()
      await flush()
      expect(mockSetConfirmKeyPin).toHaveBeenCalledTimes(1)
      expect(mockUnregisterKeyPin, 'still mounted, so still registered').not.toHaveBeenCalled()

      dispose()

      expect(mockUnregisterKeyPin, 'the registration must not outlive the mount').toHaveBeenCalledTimes(1)
    })
  })

  it('retires a displayed key confirmation with its owner and preserves the next dialog', async () => {
    const dialog = createDialogState<KeyPinConfirmState>()
    const outgoing = new AbortController()
    const successor = new AbortController()
    mockUnregisterKeyPin.mockImplementationOnce(() => outgoing.abort())
    const mountPrompt = () => createRoot((dispose) => {
      useWorkerSection({
        getUserId: () => 'u1',
        workerTrustScope: () => trustScope('u1'),
        keyPinConfirmDialog: dialog,
      })
      return dispose
    })

    const disposeFirst = mountPrompt()
    const first = mockSetConfirmKeyPin.mock.lastCall![0] as KeyPinConfirmFn
    const stale = first('worker', 'first', 'second', outgoing.signal)
    const cancelled = expect(stale).rejects.toMatchObject({ name: 'AbortError' })
    const outgoingDialog = dialog.value()!
    expect(outgoingDialog.workerId).toBe('worker')

    disposeFirst()
    await cancelled
    expect(dialog.value()).toBeNull()

    const disposeSecond = mountPrompt()
    try {
      const second = mockSetConfirmKeyPin.mock.lastCall![0] as KeyPinConfirmFn
      const pending = second('worker', 'first', 'third', successor.signal)
      const current = dialog.value()!
      outgoingDialog.resolve('accept')
      expect(dialog.value()).toBe(current)
      current.resolve('reject')
      await expect(pending).resolves.toBe('reject')
      expect(dialog.value()).toBeNull()
    }
    finally {
      disposeSecond()
    }
  })

  // The queued fetch must survive a failure of the fetch it waited behind:
  // the event that queued it still describes a change the list lacks.
  it('runs the queued fetch after the in-flight fetch fails', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)

      let fail!: () => void
      mockListWorkers.mockImplementationOnce(() => new Promise((_resolve, reject) => {
        fail = () => reject(new Error('offline'))
      }))
      mockListWorkers.mockResolvedValueOnce({
        workers: [
          worker('w1'),
          worker('w3'),
        ],
      })
      s.refetchWorkers()
      await flush()
      s.refetchWorkers()
      expect(mockListWorkers).toHaveBeenCalledTimes(2)

      fail()
      await flush()
      await flush()
      expect(mockListWorkers, 'the queued fetch runs once the failed one settles').toHaveBeenCalledTimes(3)
      expect(s.workers().map(w => w.id)).toEqual(['w1', 'w3'])
      dispose()
    })
  })

  // A sign-in that lands while the previous user's fetch runs joins the
  // queue rather than racing it, so the list that stays is the new user's.
  it('fetches again for a user who signs in while a fetch runs', async () => {
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('u1')
      let release!: () => void
      mockListWorkers.mockImplementationOnce(() => new Promise((resolve) => {
        release = () => resolve({
          workers: [
            worker('w-old'),
          ],
        })
      }))
      mockListWorkers.mockResolvedValueOnce({
        workers: [
          worker('w-new'),
        ],
      })
      const s = mount(userId)
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)

      setUserId('u2')
      await flush()
      expect(mockListWorkers, 'nothing starts while the first fetch runs').toHaveBeenCalledTimes(1)

      release()
      await flush()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      expect(s.workers().map(w => w.id)).toEqual(['w-new'])
      dispose()
    })
  })

  it('survives a failed listWorkers with an empty list', async () => {
    const logs = captureLoggerOutput()
    try {
      await createRoot(async (dispose) => {
        mockListWorkers.mockRejectedValue(new Error('offline'))
        const s = mount()
        await flush()
        expect(s.workers()).toEqual([])
        dispose()
      })
    }
    finally {
      logs.restore()
    }
  })

  // A failed read keeps the last list, and says so in the log: the sidebar
  // shows nothing to explain a worker that is missing from it.
  it('logs a failed listing and keeps the last successful list', async () => {
    const logs = captureLoggerOutput()
    try {
      await createRoot(async (dispose) => {
        const s = mount()
        await flush()
        expect(logs.messages('warn', 'workerSection')).toEqual([])

        mockListWorkers.mockRejectedValueOnce(new Error('page 2 unavailable'))
        s.refetchWorkers()
        await flush()

        expect(logs.messages('warn', 'workerSection')).toEqual(['Failed to list the workers; keeping the last list'])
        expect(s.workers().map(w => w.id)).toEqual(['w1'])
        dispose()
      })
    }
    finally {
      logs.restore()
    }
  })

  /**
   * Deregistration removes the worker from the list, and NOTHING else.
   *
   * It used to clear the worker's keyed git state too. That left every tab of
   * the worker under its repo with no branch name, for the life of the page:
   * nothing removes those tab rows, they keep `gitToplevel`, the sidebar groups
   * a tab by that field, and the branch label comes from the store. It is the
   * same defect the worker-offline sweep used to cause, from the other trigger.
   */
  it('removes the worker from the list and leaves its keyed git state alone', async () => {
    await createRoot(async (dispose) => {
      const s = mount(() => 'u1')
      await flush()

      s.openWorkerSettings(worker('w1') as never)
      const host = document.createElement('div')
      document.body.appendChild(host)
      const unmount = render(() => createComponent(s.Dialogs, {}), host)
      await flush()

      host.querySelector<HTMLButtonElement>('[data-testid="deregister-confirm"]')!.click()
      await flush()

      expect(mockDeregisterWorker).toHaveBeenCalledWith({ workerId: 'w1' })
      expect(s.workers().map(w => w.id)).toEqual([])

      unmount()
      host.remove()
      dispose()
    })
  })
})
