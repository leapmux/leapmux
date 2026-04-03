declare global {
  interface Window {
    __lm_call?: (method: string, args: unknown[]) => Promise<unknown>
  }
}

export interface TunnelConfig {
  workerId: string
  type: 'port_forward' | 'socks5'
  targetAddr: string
  targetPort: number
  bindAddr: string
  bindPort: number
  hubURL: string
  token: string
  userId: string
}

export interface TunnelInfo {
  id: string
  workerId: string
  type: 'port_forward' | 'socks5'
  bindAddr: string
  bindPort: number
  targetAddr: string
  targetPort: number
}

export function isTunnelAvailable(): boolean {
  return typeof window.__lm_call === 'function'
}

export async function createTunnel(config: TunnelConfig): Promise<TunnelInfo> {
  return window.__lm_call!('main.App.CreateTunnel', [config]) as Promise<TunnelInfo>
}

export async function deleteTunnel(tunnelId: string): Promise<void> {
  await window.__lm_call!('main.App.DeleteTunnel', [tunnelId])
}

export async function listTunnels(): Promise<TunnelInfo[]> {
  const result = await window.__lm_call!('main.App.ListTunnels', [])
  return (result as TunnelInfo[] | null) ?? []
}
