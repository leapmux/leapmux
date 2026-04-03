import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTunnel, deleteTunnel, isTunnelAvailable, listTunnels } from './tunnelApi'

function setupWailsApp(overrides: Record<string, unknown> = {}) {
  const mockApp = {
    ProxyHTTP: vi.fn(),
    CreateTunnel: vi.fn(),
    DeleteTunnel: vi.fn(),
    ListTunnels: vi.fn(),
    ...overrides,
  }
  ;(window as any).go = { main: { App: mockApp } }
  return mockApp
}

describe('tunnelApi', () => {
  afterEach(() => {
    delete (window as any).go
  })

  describe('isTunnelAvailable', () => {
    it('returns false when window.go is missing', () => {
      expect(isTunnelAvailable()).toBe(false)
    })

    it('returns true when wails bindings exist', () => {
      setupWailsApp()
      expect(isTunnelAvailable()).toBe(true)
    })
  })

  describe('createTunnel', () => {
    it('calls CreateTunnel with correct arguments', async () => {
      const mockResult = { id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '127.0.0.1', targetPort: 3000 }
      const mockApp = setupWailsApp({ CreateTunnel: vi.fn().mockResolvedValue(mockResult) })

      const config = {
        workerId: 'w1',
        type: 'port_forward' as const,
        targetAddr: '127.0.0.1',
        targetPort: 3000,
        bindAddr: '127.0.0.1',
        bindPort: 3000,
        hubURL: 'http://localhost:4327',
        token: 'tok',
        userId: 'u1',
      }

      const result = await createTunnel(config)
      expect(mockApp.CreateTunnel).toHaveBeenCalledWith(config)
      expect(result).toEqual(mockResult)
    })
  })

  describe('deleteTunnel', () => {
    it('calls DeleteTunnel with correct arguments', async () => {
      const mockApp = setupWailsApp({ DeleteTunnel: vi.fn().mockResolvedValue(undefined) })
      await deleteTunnel('t1')
      expect(mockApp.DeleteTunnel).toHaveBeenCalledWith('t1')
    })
  })

  describe('listTunnels', () => {
    it('calls ListTunnels and returns result', async () => {
      const mockList = [{ id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '127.0.0.1', targetPort: 3000 }]
      setupWailsApp({ ListTunnels: vi.fn().mockResolvedValue(mockList) })
      const result = await listTunnels()
      expect(result).toEqual(mockList)
    })

    it('returns empty array when result is null', async () => {
      setupWailsApp({ ListTunnels: vi.fn().mockResolvedValue(null) })
      const result = await listTunnels()
      expect(result).toEqual([])
    })
  })
})
