import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTunnelStore } from './tunnel.store'

const mockTunnel1 = {
  id: 't1',
  workerId: 'w1',
  type: 'port_forward' as const,
  bindAddr: '127.0.0.1',
  bindPort: 3000,
  targetAddr: '127.0.0.1',
  targetPort: 3000,
}
const mockTunnel2 = {
  id: 't2',
  workerId: 'w2',
  type: 'socks5' as const,
  bindAddr: '127.0.0.1',
  bindPort: 1080,
  targetAddr: '',
  targetPort: 0,
}

/** The three admission fields every create carries; see TunnelConfig. */
const admission = {
  expectedWorkerKeysHex: 'aa',
  expectedUserId: 'user-1',
  channelEpoch: 'epoch-1',
} as const

function tunnelConfig(overrides?: Partial<import('~/api/platformBridge').TunnelConfig>) {
  return {
    workerId: 'w1',
    type: 'port_forward' as const,
    targetAddr: '127.0.0.1',
    targetPort: 3000,
    bindAddr: '127.0.0.1',
    bindPort: 3000,
    ...admission,
    ...overrides,
  }
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
      listTunnels: vi.fn(),
    },
  }
})

describe('tunnel store', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('filters correctly in tunnelsForWorker', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    const store = createTunnelStore()

    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel1 })
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel2 })

    await store.add(tunnelConfig())
    await store.add(tunnelConfig({
      workerId: 'w2',
      type: 'socks5',
      targetAddr: '',
      targetPort: 0,
      bindPort: 1080,
    }))

    expect(store.tunnelsForWorker('w1')).toEqual([mockTunnel1])
    expect(store.tunnelsForWorker('w2')).toEqual([mockTunnel2])
    expect(store.tunnelsForWorker('w3')).toEqual([])
  })

  it('add appends to tunnel list', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel1 })

    const store = createTunnelStore()
    const result = await store.add(tunnelConfig())

    expect(result).toEqual({ kind: 'created', info: mockTunnel1 })
    expect(store.tunnels()).toEqual([mockTunnel1])
  })

  // A mismatch refused the create: it is a result the caller acts on, not a
  // row, so the list must stay empty and the fingerprints must survive.
  it('add keeps the list empty and returns the mismatch', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    const mismatch = {
      expectedFingerprint: 'deep-idea-obey-tack',
      presentedFingerprint: 'calm-lion-fern-arch',
    }
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'workerKeyMismatch', keyMismatch: mismatch })

    const store = createTunnelStore()
    const result = await store.add(tunnelConfig())

    expect(result).toEqual({ kind: 'workerKeyMismatch', keyMismatch: mismatch })
    expect(store.tunnels()).toEqual([])
  })

  it('remove filters from tunnel list', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel1 })
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel2 })
    vi.mocked(platformBridge.deleteTunnel).mockResolvedValueOnce(undefined)

    const store = createTunnelStore()
    await store.add(tunnelConfig())
    await store.add(tunnelConfig({
      workerId: 'w2',
      type: 'socks5',
      targetAddr: '',
      targetPort: 0,
      bindPort: 1080,
    }))

    await store.remove('t1')
    expect(store.tunnels()).toEqual([mockTunnel2])
  })

  it('add propagates API errors', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.createTunnel).mockRejectedValueOnce(new Error('bind failed'))

    const store = createTunnelStore()
    await expect(store.add(tunnelConfig()))
      .rejects
      .toThrow('bind failed')
    expect(store.tunnels()).toEqual([])
  })

  it('remove propagates API errors', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.createTunnel).mockResolvedValueOnce({ kind: 'created', info: mockTunnel1 })
    vi.mocked(platformBridge.deleteTunnel).mockRejectedValueOnce(new Error('not found'))

    const store = createTunnelStore()
    await store.add(tunnelConfig())

    await expect(store.remove('t1')).rejects.toThrow('not found')
    expect(store.tunnels()).toEqual([mockTunnel1])
  })

  it('refresh names the identity scope and records the channel epoch', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.listTunnels).mockResolvedValueOnce({
      tunnels: [mockTunnel1],
      channelEpoch: 'epoch-9',
    })

    const store = createTunnelStore()
    expect(store.channelEpoch()).toBe('')
    await store.refresh('user-1')

    expect(platformBridge.listTunnels).toHaveBeenCalledWith('user-1')
    expect(store.tunnels()).toEqual([mockTunnel1])
    expect(store.channelEpoch()).toBe('epoch-9')
  })

  it('refresh propagates API errors and keeps the previous rows', async () => {
    const { platformBridge } = await import('~/api/platformBridge')
    vi.mocked(platformBridge.listTunnels).mockResolvedValueOnce({
      tunnels: [mockTunnel1],
      channelEpoch: 'epoch-9',
    })
    vi.mocked(platformBridge.listTunnels).mockRejectedValueOnce(new Error('not connected'))

    const store = createTunnelStore()
    await store.refresh('user-1')
    await expect(store.refresh('user-1')).rejects.toThrow('not connected')

    expect(store.tunnels()).toEqual([mockTunnel1])
    expect(store.channelEpoch()).toBe('epoch-9')
  })
})
