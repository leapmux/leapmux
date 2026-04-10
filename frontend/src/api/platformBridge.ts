import type { BuildInfo } from '~/lib/systemInfo'
import { arrayBufferToBase64, base64ToArrayBuffer } from '~/lib/base64'
import { createLogger } from '~/lib/logger'

export type PlatformMode = 'web' | 'tauri-desktop-solo' | 'tauri-desktop-distributed' | 'tauri-mobile-distributed'

export interface PlatformCapabilities {
  mode: PlatformMode
  hubTransport: 'direct' | 'proxy'
  tunnels: boolean
  appControl: boolean
  windowControl: boolean
  systemPermissions: boolean
  localSolo: boolean
}

export interface DesktopConfig {
  mode: '' | 'solo' | 'distributed'
  hub_url: string
  window_width: number
  window_height: number
  window_maximized: boolean
}

export interface StartupInfo {
  config: DesktopConfig
  buildInfo: BuildInfo
}

/** Wire format from sidecar (snake_case JSON). */
interface StartupInfoWire {
  config: DesktopConfig
  build_info: {
    version: string
    commit_hash: string
    commit_time: string
    build_time: string
  }
}

export interface DesktopRuntimeState {
  shellMode: 'launcher' | 'solo' | 'distributed'
  connected: boolean
  hubUrl: string
  capabilities: PlatformCapabilities
}

export interface ProxyHttpResponse {
  status: number
  headers: Record<string, string>
  body: string
}

export interface TunnelConfig {
  workerId: string
  type: 'port_forward' | 'socks5'
  targetAddr: string
  targetPort: number
  bindAddr: string
  bindPort: number
  hubURL: string
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

declare global {
  interface Window {
    __TAURI_INTERNALS__?: unknown
    __leapmux_disconnectDesktop?: () => void
    __leapmux_setTheme?: (theme: 'light' | 'dark' | 'system') => void
  }
}

let runtimeStateCache: DesktopRuntimeState | null = null
let runtimeStateFetch: Promise<DesktopRuntimeState> | null = null
let sidecarLogListening = false
const mobileUserAgentRegex = /android|iphone|ipad|ipod/i
const log = createLogger('platformBridge')
const sidecarLog = createLogger('sidecar')

function caps(mode: PlatformMode, overrides: Partial<Omit<PlatformCapabilities, 'mode'>> = {}): PlatformCapabilities {
  return {
    mode,
    hubTransport: 'direct',
    tunnels: false,
    appControl: false,
    windowControl: false,
    systemPermissions: false,
    localSolo: false,
    ...overrides,
  }
}

const WEB_CAPABILITIES = caps('web')

const MOBILE_CAPABILITIES = caps('tauri-mobile-distributed', {
  appControl: true,
})

const LOCAL_DESKTOP_LAUNCHER_CAPABILITIES = caps('tauri-desktop-solo', {
  hubTransport: 'proxy',
  tunnels: true,
  appControl: true,
  windowControl: true,
  systemPermissions: true,
  localSolo: true,
})

const DISTRIBUTED_DESKTOP_CAPABILITIES = caps('tauri-desktop-distributed', {
  tunnels: true,
  appControl: true,
  windowControl: true,
  systemPermissions: true,
})

/** Default launcher window dimensions (must match tauri.conf.json). */
export const LAUNCHER_WINDOW_SIZE = { width: 900, height: 680 }

export function isTauriApp(): boolean {
  return typeof window.__TAURI_INTERNALS__ !== 'undefined'
}

export function isTunnelAvailable(): boolean {
  return getCapabilities().tunnels
}

export function isSoloTauriApp(): boolean {
  if (!isTauriApp())
    return false
  return window.location.protocol === 'tauri:' || window.location.hostname === 'tauri.localhost'
}

function isMobileViewport(): boolean {
  return mobileUserAgentRegex.test(navigator.userAgent)
}

let cachedTauriCore: Promise<typeof import('@tauri-apps/api/core')> | null = null
let cachedTauriEvents: Promise<typeof import('@tauri-apps/api/event')> | null = null
let cachedTauriDpi: Promise<typeof import('@tauri-apps/api/dpi')> | null = null
let cachedTauriWindow: Promise<typeof import('@tauri-apps/api/window')> | null = null

function loadTauriCore() {
  return (cachedTauriCore ??= import('@tauri-apps/api/core'))
}

function loadTauriEvents() {
  return (cachedTauriEvents ??= import('@tauri-apps/api/event'))
}

function loadTauriDpi() {
  return (cachedTauriDpi ??= import('@tauri-apps/api/dpi'))
}

function loadTauriWindow() {
  return (cachedTauriWindow ??= import('@tauri-apps/api/window'))
}

async function tauriInvoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const { invoke } = await loadTauriCore()
  return invoke<T>(cmd, args)
}

export function resetPlatformRuntimeState(): void {
  runtimeStateCache = null
  runtimeStateFetch = null
}

export function getCapabilities(): PlatformCapabilities {
  if (runtimeStateCache)
    return runtimeStateCache.capabilities
  if (isSoloTauriApp())
    return LOCAL_DESKTOP_LAUNCHER_CAPABILITIES
  if (isTauriApp() && isMobileViewport())
    return MOBILE_CAPABILITIES
  if (isTauriApp())
    return DISTRIBUTED_DESKTOP_CAPABILITIES
  return WEB_CAPABILITIES
}

interface SidecarLogPayload {
  level: 'debug' | 'info' | 'warn' | 'error'
  time: string
  message: string
  attrs?: string[]
}

function installSidecarLogListener(): void {
  if (sidecarLogListening || !isTauriApp())
    return
  sidecarLogListening = true

  loadTauriEvents().then(({ listen }) => {
    listen<SidecarLogPayload>('sidecar:log', (event) => {
      const { level, time, message, attrs } = event.payload
      const args: unknown[] = [time, message]
      if (attrs)
        args.push(...attrs)
      sidecarLog[level](...args)
    })
  })
}

export async function getRuntimeState(): Promise<DesktopRuntimeState> {
  if (!isTauriApp()) {
    return {
      shellMode: 'distributed',
      connected: true,
      hubUrl: window.location.origin,
      capabilities: WEB_CAPABILITIES,
    }
  }

  installSidecarLogListener()

  if (runtimeStateCache)
    return runtimeStateCache
  runtimeStateFetch ??= tauriInvoke<DesktopRuntimeState>('get_runtime_state')
    .then((state) => {
      runtimeStateCache = state
      runtimeStateFetch = null
      return state
    })
  return runtimeStateFetch
}

export async function refreshRuntimeState(): Promise<DesktopRuntimeState> {
  resetPlatformRuntimeState()
  return getRuntimeState()
}

export async function animateWindowResize(targetW: number, targetH: number, durationMs = 400): Promise<void> {
  if (!isTauriApp())
    return

  try {
    const { LogicalSize } = await loadTauriDpi()
    const { getCurrentWindow, currentMonitor } = await loadTauriWindow()
    const appWindow = getCurrentWindow()

    // Fetch scale factor, current size, and monitor info in parallel
    // to minimize IPC round-trips before the first animation frame.
    const [scaleFactor, innerSize, monitor] = await Promise.all([
      appWindow.scaleFactor(),
      appWindow.innerSize(),
      currentMonitor().catch(() => null),
    ])

    // Clamp target to the current monitor size.
    if (monitor) {
      const monitorSize = monitor.size.toLogical(scaleFactor)
      if (monitorSize.width > 0 && monitorSize.height > 0) {
        targetW = Math.min(targetW, monitorSize.width)
        targetH = Math.min(targetH, monitorSize.height)
      }
    }

    const cur = innerSize.toLogical(scaleFactor)
    if (durationMs <= 0) {
      await appWindow.setSize(new LogicalSize(targetW, targetH))
      await appWindow.center()
      return
    }

    if (cur.width === targetW && cur.height === targetH)
      return

    const startW = cur.width
    const startH = cur.height
    const steps = Math.max(Math.round(durationMs / 8), 1)
    const stepDelayMs = durationMs / steps

    for (let step = 1; step <= steps; step += 1) {
      const t = step / steps
      const eased = 1 - (1 - t) ** 3
      const w = Math.round(startW + (targetW - startW) * eased)
      const h = Math.round(startH + (targetH - startH) * eased)
      await appWindow.setSize(new LogicalSize(w, h))
      await appWindow.center()
      if (step < steps)
        await new Promise(resolve => setTimeout(resolve, stepDelayMs))
    }
    await appWindow.setSize(new LogicalSize(targetW, targetH))
    await appWindow.center()
  }
  catch (err) {
    log.warn('animateWindowResize fallback', err)
    try {
      const { LogicalSize } = await loadTauriDpi()
      const { getCurrentWindow } = await loadTauriWindow()
      const appWindow = getCurrentWindow()
      await appWindow.setSize(new LogicalSize(targetW, targetH))
      await appWindow.center()
    }
    catch (fallbackErr) {
      log.warn('animateWindowResize failed', fallbackErr)
    }
  }
}

export async function maximizeWindow(): Promise<void> {
  if (!isTauriApp())
    return

  try {
    const { getCurrentWindow } = await loadTauriWindow()
    await getCurrentWindow().maximize()
  }
  catch (err) {
    log.warn('maximizeWindow failed', err)
  }
}

export const platformBridge = {
  getCapabilities,
  getRuntimeState,
  async connectSolo(): Promise<void> {
    await tauriInvoke('connect_solo')
    await refreshRuntimeState()
  },
  async connectDistributed(hubUrl: string): Promise<void> {
    // Note: the Rust shell navigates the webview to the hub URL after
    // this resolves, so the current page is torn down — no need to
    // refresh runtime state here.
    await tauriInvoke('connect_distributed', { hubUrl })
  },
  async proxyHttp(method: string, path: string, headers: Record<string, string>, bodyBase64: string): Promise<ProxyHttpResponse> {
    return tauriInvoke('proxy_http', {
      payload: {
        method,
        path,
        headers,
        bodyBase64,
      },
    })
  },
  async openChannelRelay(): Promise<void> {
    await tauriInvoke('open_channel_relay')
  },
  async sendChannelMessage(b64Data: string): Promise<void> {
    await tauriInvoke('send_channel_message', { b64Data })
  },
  async closeChannelRelay(): Promise<void> {
    await tauriInvoke('close_channel_relay')
  },
  async getStartupInfo(): Promise<StartupInfo> {
    const wire = await tauriInvoke<StartupInfoWire>('get_startup_info')
    return {
      config: wire.config,
      buildInfo: {
        version: wire.build_info.version,
        commitHash: wire.build_info.commit_hash,
        commitTime: wire.build_info.commit_time,
        buildTime: wire.build_info.build_time,
      },
    }
  },
  async checkFullDiskAccess(): Promise<boolean> {
    return tauriInvoke('check_full_disk_access')
  },
  async openFullDiskAccessSettings(): Promise<void> {
    await tauriInvoke('open_full_disk_access_settings')
  },
  async restart(): Promise<void> {
    await tauriInvoke('restart_app')
  },
  async switchMode(): Promise<void> {
    await tauriInvoke('switch_mode')
    await refreshRuntimeState()
  },
  async createTunnel(config: TunnelConfig): Promise<TunnelInfo> {
    return tauriInvoke('create_tunnel', { config })
  },
  async deleteTunnel(tunnelId: string): Promise<void> {
    await tauriInvoke('delete_tunnel', { tunnelId })
  },
  async listTunnels(): Promise<TunnelInfo[]> {
    return (await tauriInvoke<TunnelInfo[]>('list_tunnels')) ?? []
  },
  async onEvent(event: string, callback: (...args: unknown[]) => void): Promise<() => void> {
    if (!isTauriApp())
      return () => {}
    const { listen } = await loadTauriEvents()
    return listen(event, payload => callback(payload.payload))
  },
}

export const desktopFetch: typeof globalThis.fetch = async (input, init) => {
  const url = typeof input === 'string' ? input : (input as Request).url
  const method = init?.method ?? 'POST'
  const headers = Object.fromEntries(new Headers(init?.headers).entries())
  const body = init?.body ? arrayBufferToBase64(init.body as ArrayBuffer | Uint8Array | string) : ''
  const parsed = new URL(url)
  const resp = await platformBridge.proxyHttp(
    method,
    parsed.pathname + parsed.search,
    headers,
    body,
  )

  return new Response(base64ToArrayBuffer(resp.body), {
    status: resp.status,
    headers: resp.headers,
  })
}
