import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTunnelStore } from './tunnel.store'

const mockTunnel1 = { id: 't1', workerId: 'w1', type: 'port_forward' as const, bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '127.0.0.1', targetPort: 3000 }
const mockTunnel2 = { id: 't2', workerId: 'w2', type: 'socks5' as const, bindAddr: '127.0.0.1', bindPort: 1080, targetAddr: '', targetPort: 0 }

vi.mock('~/api/tunnelApi', () => ({
  createTunnel: vi.fn(),
  deleteTunnel: vi.fn(),
  listTunnels: vi.fn(),
}))

describe('tunnel store', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('tunnelsForWorker filters correctly', async () => {
    const { createTunnel } = await import('~/api/tunnelApi')
    const store = createTunnelStore()

    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel1)
    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel2)

    await store.add({ workerId: 'w1', type: 'port_forward', targetAddr: '127.0.0.1', targetPort: 3000, bindAddr: '127.0.0.1', bindPort: 3000, hubURL: '', token: '', userId: '' })
    await store.add({ workerId: 'w2', type: 'socks5', targetAddr: '', targetPort: 0, bindAddr: '127.0.0.1', bindPort: 1080, hubURL: '', token: '', userId: '' })

    expect(store.tunnelsForWorker('w1')).toEqual([mockTunnel1])
    expect(store.tunnelsForWorker('w2')).toEqual([mockTunnel2])
    expect(store.tunnelsForWorker('w3')).toEqual([])
  })

  it('add appends to tunnel list', async () => {
    const { createTunnel } = await import('~/api/tunnelApi')
    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel1)

    const store = createTunnelStore()
    const result = await store.add({ workerId: 'w1', type: 'port_forward', targetAddr: '127.0.0.1', targetPort: 3000, bindAddr: '127.0.0.1', bindPort: 3000, hubURL: '', token: '', userId: '' })

    expect(result).toEqual(mockTunnel1)
    expect(store.tunnels()).toEqual([mockTunnel1])
  })

  it('remove filters from tunnel list', async () => {
    const { createTunnel, deleteTunnel } = await import('~/api/tunnelApi')
    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel1)
    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel2)
    vi.mocked(deleteTunnel).mockResolvedValueOnce(undefined)

    const store = createTunnelStore()
    await store.add({ workerId: 'w1', type: 'port_forward', targetAddr: '127.0.0.1', targetPort: 3000, bindAddr: '127.0.0.1', bindPort: 3000, hubURL: '', token: '', userId: '' })
    await store.add({ workerId: 'w2', type: 'socks5', targetAddr: '', targetPort: 0, bindAddr: '127.0.0.1', bindPort: 1080, hubURL: '', token: '', userId: '' })

    await store.remove('t1')
    expect(store.tunnels()).toEqual([mockTunnel2])
  })

  it('add propagates API errors', async () => {
    const { createTunnel } = await import('~/api/tunnelApi')
    vi.mocked(createTunnel).mockRejectedValueOnce(new Error('bind failed'))

    const store = createTunnelStore()
    await expect(store.add({ workerId: 'w1', type: 'port_forward', targetAddr: '127.0.0.1', targetPort: 3000, bindAddr: '127.0.0.1', bindPort: 3000, hubURL: '', token: '', userId: '' }))
      .rejects
      .toThrow('bind failed')
    expect(store.tunnels()).toEqual([])
  })

  it('remove propagates API errors', async () => {
    const { createTunnel, deleteTunnel } = await import('~/api/tunnelApi')
    vi.mocked(createTunnel).mockResolvedValueOnce(mockTunnel1)
    vi.mocked(deleteTunnel).mockRejectedValueOnce(new Error('not found'))

    const store = createTunnelStore()
    await store.add({ workerId: 'w1', type: 'port_forward', targetAddr: '127.0.0.1', targetPort: 3000, bindAddr: '127.0.0.1', bindPort: 3000, hubURL: '', token: '', userId: '' })

    await expect(store.remove('t1')).rejects.toThrow('not found')
    expect(store.tunnels()).toEqual([mockTunnel1])
  })
})
