/// <reference types="vitest/globals" />
import { createComponent, createRoot, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createDialogState } from '~/hooks/createDialogState'
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
vi.mock('~/stores/tunnel.store', () => ({
  createTunnelStore: () => ({
    tunnels: vi.fn(),
  }),
}))

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function worker(id: string, online = true) {
  return { id, online, name: id }
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
})

/**
 * The worker registry, lifted out of `AppShell`. It shares nothing with the
 * rest of that component — no tabs, no layout, no projection — so it is
 * testable on its own, which it was not while it lived inline.
 */
describe('useWorkerSection', () => {
  function mount(getUserId: () => string = () => 'u1') {
    return useWorkerSection({
      getUserId,
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
    await createRoot(async (dispose) => {
      mockListWorkers.mockRejectedValue(new Error('offline'))
      const s = mount()
      await flush()
      expect(s.workers()).toEqual([])
      dispose()
    })
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
