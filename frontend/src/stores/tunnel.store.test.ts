import type { CreateTunnelResult, ListTunnelsResult, TunnelConfig, TunnelInfo } from '~/api/platformBridge'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { createEffect, createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { platformBridge } from '~/api/platformBridge'
import { storageAccountFor, TEST_HUB } from '~/lib/browserStorage'
import { deferred, settle } from '~/test-support/async'
import { createTunnelStore } from './tunnel.store'

const config: TunnelConfig = {
  workerId: 'w1',
  type: 'port_forward',
  bindAddr: '127.0.0.1',
  bindPort: 3000,
  targetAddr: '127.0.0.1',
  targetPort: 3000,
}

const active: TunnelInfo = { ...config, id: 'active' }
const stopped: TunnelInfo = {
  ...config,
  id: 'stopped',
  keyMismatch: { expectedFingerprint: 'expected', presentedFingerprint: 'presented' },
}

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    desktopHub: () => null,
    platformBridge: {
      ...actual.platformBridge,
      createTunnel: vi.fn(),
      deleteTunnel: vi.fn(),
      deleteTunnels: vi.fn(),
      listTunnels: vi.fn(),
    },
  }
})

function listing(...tunnels: TunnelInfo[]): ListTunnelsResult {
  return { tunnels, channelEpoch: 'epoch' }
}

describe('createTunnelStore', () => {
  let scope: WorkerTrustScope
  let controller: AbortController
  let store: ReturnType<typeof createTunnelStore>

  function adopt(userId = 'user'): WorkerTrustScope {
    controller.abort()
    controller = new AbortController()
    scope = {
      userId,
      storageAccount: storageAccountFor(TEST_HUB, userId),
      generation: scope.generation + 1,
      signal: controller.signal,
    }
    return scope
  }

  beforeEach(() => {
    vi.resetAllMocks()
    controller = new AbortController()
    scope = {
      userId: 'user',
      storageAccount: storageAccountFor(TEST_HUB, 'user'),
      generation: 1,
      signal: controller.signal,
    }
    store = createTunnelStore(() => scope)
  })

  it('takes the created rows from the scoped native snapshot', async () => {
    const normalized = { ...active, bindPort: 3500 }
    vi.mocked(platformBridge.createTunnel).mockResolvedValue({ kind: 'created', info: active })
    vi.mocked(platformBridge.listTunnels).mockResolvedValue(listing(normalized))

    await expect(store.add(config, scope)).resolves.toEqual({ kind: 'created', info: active })
    expect(platformBridge.listTunnels).toHaveBeenCalledExactlyOnceWith(scope)
    expect(store.tunnelsForWorker('w1')).toEqual([normalized])
    expect(store.tunnelsForWorker('missing')).toEqual([])
  })

  it('returns a mismatch without synthesizing a stopped row', async () => {
    const result = {
      kind: 'workerKeyMismatch',
      keyMismatch: { expectedFingerprint: 'expected', presentedFingerprint: 'presented' },
    } as const
    vi.mocked(platformBridge.createTunnel).mockResolvedValue(result)
    vi.mocked(platformBridge.listTunnels).mockResolvedValue(listing())

    await expect(store.add(config, scope)).resolves.toEqual(result)
    expect(store.tunnels()).toEqual([])
  })

  it('reconciles the native rows after one deletion fails', async () => {
    vi.mocked(platformBridge.listTunnels)
      .mockResolvedValueOnce(listing(active, stopped))
      .mockResolvedValueOnce(listing(stopped))
    vi.mocked(platformBridge.deleteTunnels).mockResolvedValueOnce([
      { status: 'fulfilled', value: undefined },
      {
        status: 'rejected',
        reason: new Error('delete failed'),
      },
    ])

    await store.refresh(scope)
    await expect(store.removeAllForWorker('w1', scope)).rejects.toThrow('One tunnel could not be deleted')
    expect(platformBridge.deleteTunnels).toHaveBeenCalledExactlyOnceWith('w1', scope)
    expect(store.tunnels()).toEqual([stopped])
  })

  it('reconciles a single deletion and preserves the failure for the confirmation', async () => {
    vi.mocked(platformBridge.listTunnels).mockResolvedValue(listing(stopped))
    vi.mocked(platformBridge.deleteTunnel).mockRejectedValue(new Error('desktop reset blocked'))
    await expect(store.remove('stopped', scope)).rejects.toThrow('desktop reset blocked')
    expect(store.tunnels()).toEqual([stopped])
  })

  it('keeps current rows on refresh failure', async () => {
    vi.mocked(platformBridge.listTunnels)
      .mockResolvedValueOnce(listing(active))
      .mockRejectedValueOnce(new Error('not connected'))

    await store.refresh(scope)
    await expect(store.refresh(scope)).rejects.toThrow('not connected')
    expect(store.tunnels()).toEqual([active])
  })

  it('hides active and stopped rows immediately even if the successor list fails', async () => {
    const successor = deferred<ListTunnelsResult>()
    vi.mocked(platformBridge.listTunnels)
      .mockResolvedValueOnce(listing(active, stopped))
      .mockReturnValueOnce(successor.promise)
    await store.refresh(scope)

    const outgoing = scope
    const refresh = store.refresh(adopt())
    expect(store.tunnels()).toEqual([])
    const failure = expect(refresh).rejects.toThrow('reset blocked')
    successor.reject(new Error('reset blocked'))
    await failure
    await expect(store.removeAllForWorker('w1', outgoing)).rejects.toThrow('account changed')
    expect(platformBridge.deleteTunnels).not.toHaveBeenCalled()
    expect(store.tunnels()).toEqual([])
  })

  it('discards out-of-order list and create replies from a retired scope', async () => {
    const oldList = deferred<ListTunnelsResult>()
    const oldCreate = deferred<CreateTunnelResult>()
    const oldCalled = deferred<void>()
    const oldScope = scope
    vi.mocked(platformBridge.listTunnels)
      .mockImplementationOnce(() => {
        oldCalled.resolve()
        return oldList.promise
      })
      .mockResolvedValueOnce(listing(stopped))
    vi.mocked(platformBridge.createTunnel).mockReturnValueOnce(oldCreate.promise)
    const oldRead = expect(store.refresh(oldScope)).rejects.toThrow('account changed')
    const oldWrite = expect(store.add(config, oldScope)).rejects.toThrow('account changed')
    await oldCalled.promise

    await store.refresh(adopt())
    oldList.resolve(listing(active))
    oldCreate.resolve({ kind: 'created', info: active })

    await Promise.all([oldRead, oldWrite])
    expect(store.tunnels()).toEqual([stopped])
    expect(platformBridge.listTunnels).toHaveBeenCalledTimes(2)
  })

  it('starts a new read when an observer invalidates the just-published snapshot', async () => {
    vi.mocked(platformBridge.listTunnels)
      .mockResolvedValueOnce(listing(active))
      .mockResolvedValueOnce(listing(stopped))
    let invalidated: Promise<void> | undefined
    const dispose = createRoot((dispose) => {
      createEffect(() => {
        if (store.tunnels().includes(active))
          invalidated = store.refresh(scope, 'invalidate')
      })
      return dispose
    })

    try {
      await store.refresh(scope)
      expect(invalidated).toBeDefined()
      await invalidated
      expect(platformBridge.listTunnels).toHaveBeenCalledTimes(2)
      expect(store.tunnels()).toEqual([stopped])
    }
    finally {
      dispose()
    }
  })

  it('coalesces refresh and retains a single trailing read after invalidation', async () => {
    const first = deferred<ListTunnelsResult>()
    const trailing = deferred<ListTunnelsResult>()
    const firstCalled = deferred<void>()
    const trailingCalled = deferred<void>()
    vi.mocked(platformBridge.listTunnels)
      .mockImplementationOnce(() => {
        firstCalled.resolve()
        return first.promise
      })
      .mockImplementationOnce(() => {
        trailingCalled.resolve()
        return trailing.promise
      })
    const initial = store.refresh(scope)
    await firstCalled.promise
    const plain = store.refresh(scope)
    const hint = store.refresh(scope, 'invalidate')
    const change = store.refresh(scope, 'invalidate')
    first.resolve(listing(active))
    await trailingCalled.promise
    expect(store.tunnels()).toEqual([])

    trailing.resolve(listing(stopped))
    await Promise.all([initial, plain, hint, change])
    await settle()
    expect(platformBridge.listTunnels).toHaveBeenCalledTimes(2)
    expect(store.tunnels()).toEqual([stopped])
  })
})
