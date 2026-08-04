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

/**
 * Largest body `unloadTransport` will attempt.
 *
 * `keepalive` requests share a 64 KiB budget across ALL in-flight ones, and a
 * request over it fails outright — so a caller must fall back rather than
 * discover this during unload, when there is no second chance.
 */
export const MAX_KEEPALIVE_BODY_BYTES = 60 * 1024

/**
 * A transport for requests issued from a `pagehide` handler.
 *
 * Identical to `transport` except for `keepalive: true`. That flag is the whole
 * point: a normal fetch started while the page is unloading is CANCELLED with
 * it, so an op enqueued at unload never reaches the hub — the browser tears the
 * request down along with the document. `keepalive` asks the browser to let it
 * complete after the page is gone.
 *
 * A separate transport rather than a flag on the shared one because keepalive
 * carries a hard 64 KiB budget shared across every in-flight keepalive request.
 * Making it the default would silently cap ordinary traffic (a materialized
 * snapshot is routinely larger); confining it to the unload path keeps the cap
 * where the payload is a handful of ops.
 *
 * Everything else — auth via the same credential fetch, the Connect protocol
 * framing, the error interceptor — is shared, which is why this is a transport
 * and not a hand-rolled `sendBeacon`: a beacon cannot set headers, so it would
 * need its own endpoint and its own auth story.
 */
export const unloadTransport = createConnectTransport({
  baseUrl: window.location.origin,
  fetch: (input, init) => getTransportFetch()(input, { ...init, keepalive: true }),
  interceptors: [errorInterceptor],
  // No timeout: the page is going away, so there is nothing left to time out
  // INTO. The browser bounds the request itself once the document is gone.
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

/**
 * How long the shell waits for the CRDT bootstrap to deliver the active
 * workspace before it paints anyway (milliseconds).
 *
 * Deliberately a multiple of the RPC deadline rather than equal to it: this
 * budget covers a socket connect, a Noise handshake and a full user-state
 * materialization, so a value tight enough to catch a slow-but-working
 * bootstrap would flash the empty state on every cold load. It is a watchdog
 * against a bootstrap that will never arrive, not a latency target — nothing
 * is cancelled when it fires, and a late bootstrap still fills the projection
 * in. Scales with `TIMEOUT_MULTIPLIER` so CI and E2E inherit the same slack as
 * every other deadline.
 */
export function workspaceBootstrapTimeoutMs(): number {
  return apiLoadingTimeoutMs() * 3
}
