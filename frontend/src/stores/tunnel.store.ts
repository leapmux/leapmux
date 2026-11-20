import type { CreateTunnelResult, TunnelConfig, TunnelInfo } from '~/api/platformBridge'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'

export interface TunnelStore {
  tunnels: () => TunnelInfo[]
  tunnelsForWorker: (workerId: string) => TunnelInfo[]
  /**
   * The channel epoch of the last listing. A create is admitted against it,
   * so the sidecar can refuse a submission fenced on a listing that has since
   * been invalidated. Empty before the first listing.
   */
  channelEpoch: () => string
  /**
   * Re-read the tunnels one identity owns. `expectedUserId` is the identity
   * scope: the sidecar answers only the rows whose admitting user matches it,
   * and refuses a listing that names no user.
   */
  refresh: (expectedUserId: string) => Promise<void>
  add: (config: TunnelConfig) => Promise<CreateTunnelResult>
  remove: (tunnelId: string) => Promise<void>
  removeAllForWorker: (workerId: string) => Promise<void>
}

export function createTunnelStore(): TunnelStore {
  const [tunnels, setTunnels] = createSignal<TunnelInfo[]>([])
  const [channelEpoch, setChannelEpoch] = createSignal('')

  return {
    tunnels,
    tunnelsForWorker: (workerId: string) => tunnels().filter(t => t.workerId === workerId),
    channelEpoch,
    refresh: async (expectedUserId: string) => {
      const result = await platformBridge.listTunnels(expectedUserId)
      setTunnels(result.tunnels)
      setChannelEpoch(result.channelEpoch)
    },
    add: async (config: TunnelConfig) => {
      const result = await platformBridge.createTunnel(config)
      // A mismatch refused the create, so there is no row to append. The
      // caller shows the two fingerprints and decides what to do next.
      if (result.kind === 'workerKeyMismatch')
        return result
      setTunnels(prev => [...prev, result.info])
      return result
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
