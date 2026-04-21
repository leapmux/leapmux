import type { Interceptor } from '@connectrpc/connect'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { desktopFetch, getCapabilities, isTauriApp } from '~/api/platformBridge'
import { UserService } from '~/generated/leapmux/v1/user_pb'

// Callbacks for auth state changes (set by AuthContext)
let onAuthError: (() => void) | null = null

export function setOnAuthError(callback: () => void): void {
  onAuthError = callback
}

const errorInterceptor: Interceptor = next => async (req) => {
  try {
    return await next(req)
  }
  catch (err) {
    // Auto-logout on unauthenticated errors (expired/invalid session)
    if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
      onAuthError?.()
    }
    throw err
  }
}

// Wrap native fetch to always include credentials (cookies).
const credentialFetch: typeof globalThis.fetch = (input, init) => {
  return globalThis.fetch(input, { ...init, credentials: 'include' })
}

function getTransportFetch(): typeof globalThis.fetch {
  if (!isTauriApp())
    return credentialFetch

  // Return a wrapper that checks capabilities on each call so the
  // transport picks up runtime-state changes (e.g. switching from
  // launcher → solo mode). The eager check at module-init time would
  // use stale heuristics—especially in dev mode where the webview
  // loads from http://localhost instead of tauri://localhost.
  return (input, init) => {
    const capabilities = getCapabilities()
    if (capabilities.hubTransport === 'proxy')
      return desktopFetch(input, init)
    return credentialFetch(input, init)
  }
}

export const transport = createConnectTransport({
  baseUrl: window.location.origin,
  fetch: getTransportFetch(),
  interceptors: [errorInterceptor],
  defaultTimeoutMs: 30_000,
})

// ---------------------------------------------------------------------------
// Dynamic timeout configuration
// ---------------------------------------------------------------------------

/**
 * Multiplier applied to backend timeouts for frontend RPC deadlines.
 *
 * Invariant: the frontend always waits for (backend timeout × multiplier),
 * so the backend has time to surface a DeadlineExceeded response before the
 * frontend aborts the call on its own.
 */
const TIMEOUT_MULTIPLIER = 1.5

export interface TimeoutConfig {
  apiTimeoutSeconds: number
}

const timeoutConfig: TimeoutConfig = {
  apiTimeoutSeconds: 10,
}

/** Load timeout configuration from the server. Call after authentication. */
export async function loadTimeouts(): Promise<void> {
  try {
    const client = createClient(UserService, transport)
    const resp = await client.getTimeouts({})
    if (resp.apiTimeoutSeconds > 0)
      timeoutConfig.apiTimeoutSeconds = resp.apiTimeoutSeconds
  }
  catch {
    // Use defaults if the server doesn't support this endpoint yet.
  }
}

/**
 * Canonical frontend RPC deadline (milliseconds).
 *
 * Used both as the `timeoutMs` for unary RPC calls and as the UI loading
 * signal budget — by design they match, so the RPC's own DeadlineExceeded
 * error path always wins over a forced loading-state clear.
 */
export function apiLoadingTimeoutMs(): number {
  return Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.apiTimeoutSeconds * 1000)
}
