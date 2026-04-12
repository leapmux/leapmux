import type { TunnelConfig, TunnelInfo } from '~/api/platformBridge'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'

export interface TunnelStore {
  tunnels: () => TunnelInfo[]
  tunnelsForWorker: (workerId: string) => TunnelInfo[]
  refresh: () => Promise<void>
  add: (config: TunnelConfig) => Promise<TunnelInfo>
  remove: (tunnelId: string) => Promise<void>
  removeAllForWorker: (workerId: string) => Promise<void>
}

export function createTunnelStore(): TunnelStore {
  const [tunnels, setTunnels] = createSignal<TunnelInfo[]>([])

  return {
    tunnels,
    tunnelsForWorker: (workerId: string) => tunnels().filter(t => t.workerId === workerId),
    refresh: async () => {
      setTunnels(await platformBridge.listTunnels())
    },
    add: async (config: TunnelConfig) => {
      const tunnel = await platformBridge.createTunnel(config)
      setTunnels(prev => [...prev, tunnel])
      return tunnel
    },
    remove: async (tunnelId: string) => {
      await platformBridge.deleteTunnel(tunnelId)
      setTunnels(prev => prev.filter(t => t.id !== tunnelId))
    },
    removeAllForWorker: async (workerId: string) => {
      const toDelete = tunnels().filter(t => t.workerId === workerId)
      await Promise.all(toDelete.map(t => platformBridge.deleteTunnel(t.id)))
      setTunnels(prev => prev.filter(t => t.workerId !== workerId))
    },
  }
}
