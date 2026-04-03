import { isWailsApp } from '~/api/desktopBridge'

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
  return isWailsApp()
}

export async function createTunnel(config: TunnelConfig): Promise<TunnelInfo> {
  return window.go!.main.App.CreateTunnel(config) as Promise<TunnelInfo>
}

export async function deleteTunnel(tunnelId: string): Promise<void> {
  await window.go!.main.App.DeleteTunnel(tunnelId)
}

export async function listTunnels(): Promise<TunnelInfo[]> {
  const result = await window.go!.main.App.ListTunnels()
  return (result as TunnelInfo[] | null) ?? []
}
