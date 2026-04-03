import type { TunnelConfig, TunnelInfo } from '~/api/tunnelApi'
import { createSignal } from 'solid-js'
import { createTunnel, deleteTunnel, listTunnels } from '~/api/tunnelApi'

export interface TunnelStore {
  tunnels: () => TunnelInfo[]
  tunnelsForWorker: (workerId: string) => TunnelInfo[]
  refresh: () => Promise<void>
  add: (config: TunnelConfig) => Promise<TunnelInfo>
  remove: (tunnelId: string) => Promise<void>
}

export function createTunnelStore(): TunnelStore {
  const [tunnels, setTunnels] = createSignal<TunnelInfo[]>([])

  return {
    tunnels,
    tunnelsForWorker: (workerId: string) => tunnels().filter(t => t.workerId === workerId),
    refresh: async () => {
      setTunnels(await listTunnels())
    },
    add: async (config: TunnelConfig) => {
      const tunnel = await createTunnel(config)
      setTunnels(prev => [...prev, tunnel])
      return tunnel
    },
    remove: async (tunnelId: string) => {
      await deleteTunnel(tunnelId)
      setTunnels(prev => prev.filter(t => t.id !== tunnelId))
    },
  }
}
