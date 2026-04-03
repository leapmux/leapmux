/**
 * Desktop bridge for communicating with the Wails Go backend.
 *
 * When the SPA runs inside the Wails desktop app, `window.go.main.App.*`
 * bindings are available natively. This module provides typed wrappers
 * and a custom fetch implementation that proxies ConnectRPC requests
 * through the Go backend (which forwards them to the Hub via Unix socket
 * or HTTPS, depending on the mode).
 */

// ---------------------------------------------------------------------------
// Type declarations for Wails bindings
// ---------------------------------------------------------------------------

declare global {
  interface Window {
    go?: {
      main: {
        App: {
          ProxyHTTP: (method: string, path: string, headersJSON: string, bodyBase64: string) => Promise<{
            status: number
            headers: Record<string, string>
            body: string
          }>
          OpenChannelRelay: (token: string) => Promise<void>
          SendChannelMessage: (b64Data: string) => Promise<void>
          CloseChannelRelay: () => Promise<void>
          ConnectSolo: () => Promise<void>
          ConnectDistributed: (hubURL: string) => Promise<void>
          GetConfig: () => Promise<{ mode: string, hub_url: string, window_width: number, window_height: number }>
          GetVersion: () => Promise<string>
          GetBuildInfo: () => Promise<{ version: string, commit_hash: string, commit_time: string, build_time: string }>
          CheckFullDiskAccess: () => Promise<boolean>
          OpenFullDiskAccessSettings: () => Promise<void>
          Restart: () => Promise<void>
          SwitchMode: () => Promise<void>
          GetHubURL: () => Promise<string>
          IsConnected: () => Promise<boolean>
          CreateTunnel: (config: unknown) => Promise<unknown>
          DeleteTunnel: (tunnelId: string) => Promise<void>
          ListTunnels: () => Promise<unknown[]>
        }
      }
    }
    runtime?: {
      EventsOn?: (event: string, callback: (...args: unknown[]) => void) => void
      EventsOff?: (event: string) => void
      BrowserOpenURL?: (url: string) => void
      Quit?: () => void
      WindowSetSize?: (width: number, height: number) => void
      WindowGetSize?: () => Promise<{ w: number, h: number }>
      WindowCenter?: () => void
    }
  }
}

// ---------------------------------------------------------------------------
// Desktop detection
// ---------------------------------------------------------------------------

/** Returns true when running inside the Wails desktop app. */
export function isWailsApp(): boolean {
  // Check URL scheme first — available immediately, even before Wails
  // injects window.go bindings (which happens asynchronously on reload).
  return window.location.protocol === 'wails:'
    || typeof window.go?.main?.App?.ProxyHTTP === 'function'
}

/**
 * Returns a promise that resolves once `window.go.main.App` is available.
 * Wails injects bindings asynchronously after page load; this polls until ready.
 * Note: window.runtime is NOT waited for here — animateWindowResize handles
 * its absence gracefully. Waiting for it caused hangs on page reload.
 */
export function waitForWailsBindings(): Promise<void> {
  if (typeof window.go?.main?.App?.ProxyHTTP === 'function')
    return Promise.resolve()
  return new Promise((resolve) => {
    const check = setInterval(() => {
      if (typeof window.go?.main?.App?.ProxyHTTP === 'function') {
        clearInterval(check)
        resolve()
      }
    }, 50)
  })
}

// ---------------------------------------------------------------------------
// Base64 helpers
// ---------------------------------------------------------------------------

export function arrayBufferToBase64(buf: ArrayBuffer | Uint8Array | string | null | undefined): string {
  if (!buf)
    return ''
  if (typeof buf === 'string') {
    // Already a string (e.g. JSON body) — encode as UTF-8 bytes.
    const bytes = new TextEncoder().encode(buf)
    return uint8ArrayToBase64(bytes)
  }
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  return uint8ArrayToBase64(bytes)
}

function uint8ArrayToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i])
  }
  return btoa(binary)
}

export function base64ToArrayBuffer(b64: string): ArrayBuffer {
  if (!b64)
    return new ArrayBuffer(0)
  const binary = atob(b64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes.buffer
}

// ---------------------------------------------------------------------------
// Window animation
// ---------------------------------------------------------------------------

/**
 * Animate the window from its current size to the target size over the given
 * duration (ms), keeping it centered. Uses an ease-out cubic curve.
 * This function never throws — errors result in a best-effort snap.
 */
export async function animateWindowResize(targetW: number, targetH: number, durationMs = 400): Promise<void> {
  try {
    const rt = window.runtime
    if (!rt?.WindowGetSize || !rt?.WindowSetSize)
      return

    // Instant snap when duration is 0.
    if (durationMs <= 0) {
      rt.WindowSetSize(targetW, targetH)
      rt.WindowCenter?.()
      return
    }

    const cur = await rt.WindowGetSize()
    if (cur.w === targetW && cur.h === targetH)
      return

    await new Promise<void>((resolve) => {
      const startW = cur.w
      const startH = cur.h
      const startTime = performance.now()

      function step(now: number) {
        const t = Math.min((now - startTime) / durationMs, 1)
        const eased = 1 - (1 - t) ** 3
        const w = Math.round(startW + (targetW - startW) * eased)
        const h = Math.round(startH + (targetH - startH) * eased)
        rt!.WindowSetSize!(w, h)
        rt!.WindowCenter?.()
        if (t < 1)
          requestAnimationFrame(step)
        else
          resolve()
      }

      requestAnimationFrame(step)
    })
  }
  catch {
    try {
      window.runtime?.WindowSetSize?.(targetW, targetH)
      window.runtime?.WindowCenter?.()
    }
    catch { /* ignore */ }
  }
}

// ---------------------------------------------------------------------------
// Custom fetch for ConnectRPC
// ---------------------------------------------------------------------------

/**
 * Custom fetch implementation that proxies HTTP requests through Wails
 * Go bindings. Used by ConnectRPC transport when running in desktop mode.
 */
export const desktopFetch: typeof globalThis.fetch = async (input, init) => {
  const url = typeof input === 'string' ? input : (input as Request).url
  const method = init?.method ?? 'POST'
  const headers = Object.fromEntries(new Headers(init?.headers).entries())
  const body = init?.body ? arrayBufferToBase64(init.body as ArrayBuffer | Uint8Array | string) : ''

  const parsed = new URL(url)
  const resp = await window.go!.main.App.ProxyHTTP(
    method,
    parsed.pathname + parsed.search,
    JSON.stringify(headers),
    body,
  )

  return new Response(base64ToArrayBuffer(resp.body), {
    status: resp.status,
    headers: resp.headers,
  })
}
