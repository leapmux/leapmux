import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTunnel, deleteTunnel, isTunnelAvailable, listTunnels } from './tunnelApi'

describe('tunnelApi', () => {
  afterEach(() => {
    delete (window as any).__lm_call
  })

  describe('isTunnelAvailable', () => {
    it('returns false when __lm_call is missing', () => {
      expect(isTunnelAvailable()).toBe(false)
    })

    it('returns true when __lm_call exists', () => {
      ;(window as any).__lm_call = vi.fn()
      expect(isTunnelAvailable()).toBe(true)
    })
  })

  describe('createTunnel', () => {
    it('calls __lm_call with correct arguments', async () => {
      const mockResult = { id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '127.0.0.1', targetPort: 3000 }
      ;(window as any).__lm_call = vi.fn().mockResolvedValue(mockResult)

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
      expect(window.__lm_call).toHaveBeenCalledWith('main.App.CreateTunnel', [config])
      expect(result).toEqual(mockResult)
    })
  })

  describe('deleteTunnel', () => {
    it('calls __lm_call with correct arguments', async () => {
      ;(window as any).__lm_call = vi.fn().mockResolvedValue(undefined)
      await deleteTunnel('t1')
      expect(window.__lm_call).toHaveBeenCalledWith('main.App.DeleteTunnel', ['t1'])
    })
  })

  describe('listTunnels', () => {
    it('calls __lm_call with correct arguments', async () => {
      const mockList = [{ id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '127.0.0.1', targetPort: 3000 }]
      ;(window as any).__lm_call = vi.fn().mockResolvedValue(mockList)
      const result = await listTunnels()
      expect(window.__lm_call).toHaveBeenCalledWith('main.App.ListTunnels', [])
      expect(result).toEqual(mockList)
    })

    it('returns empty array when result is null', async () => {
      ;(window as any).__lm_call = vi.fn().mockResolvedValue(null)
      const result = await listTunnels()
      expect(result).toEqual([])
    })
  })
})
