import type { Interceptor } from '@connectrpc/connect'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { desktopFetch, getCapabilities, isTauriApp } from '~/api/platformBridge'
import { UserService } from '~/generated/leapmux/v1/user_pb'
import { isElevationRequired, promptForElevation } from '~/lib/elevationPrompt'

// Callbacks for auth state changes (set by AuthContext)
let onAuthError: (() => void) | null = null

export function setOnAuthError(callback: () => void): void {
  onAuthError = callback
}

/**
 * The hub's marker for an Unauthenticated whose subject is a credential the
 * REQUEST carried -- a step-up password, a step-up passkey assertion -- and
 * not the session that made it. Mirrors service.CredentialRejectedHeader.
 *
 * Without it, mistyping the password in a verification prompt would end the
 * very session the prompt exists to protect: the blanket rule below reads
 * every Unauthenticated as "your session is gone".
 */
const CREDENTIAL_REJECTED_HEADER = 'leapmux-credential-rejected'

/**
 * Whether a failure means THIS SESSION is gone, and the user must be signed
 * out.
 *
 * Exported because it is the decision, not an implementation detail of the
 * interceptor below: an Unauthenticated carrying the hub's
 * credential-rejected marker is a wrong answer to a prompt, and signing the
 * user out for it ends the session the prompt was protecting.
 */
export function isSessionEnded(err: unknown): boolean {
  return err instanceof ConnectError
    && err.code === Code.Unauthenticated
    && err.metadata.get(CREDENTIAL_REJECTED_HEADER) === null
}

const errorInterceptor: Interceptor = next => async (req) => {
  try {
    return await next(req)
  }
  catch (err) {
    if (isSessionEnded(err))
      onAuthError?.()
    throw err
  }
}

/**
 * Opens the step-up prompt on the hub's elevation refusal, and runs the
 * refused request again once a factor is proven.
 *
 * ATTEMPT-THEN-PROMPT, not check-then-attempt. A client's copy of the
 * elevation deadline can be stale by a whole page lifetime, so a client that
 * decided for itself would either prompt for a window the user already holds
 * or let them fill in a form the hub is about to refuse. The hub's refusal
 * carries a marker, and that refusal is the only trigger.
 *
 * HERE rather than at each call site, which is the whole point. Every
 * sensitive call used to opt in by wrapping itself in `gate.run(...)`, so one
 * that forgot rendered the hub's raw refusal text beside a form with no way
 * forward — and nothing in the frontend said which procedures were gated, so
 * no guard could check. An interceptor needs no such list: the hub's own
 * marker is the classification.
 *
 * EXACTLY ONE retry. Every action behind this is a deliberate mutation the
 * user already confirmed, and the first attempt changed nothing — the hub
 * refused it before doing any work. A second refusal is reported.
 *
 * STREAMS are excluded. A stream's request carries an async iterable of
 * messages that cannot be consumed twice, so replaying one would send an
 * empty body. No streaming procedure is elevation-gated; this is the guard
 * that keeps that true if one ever is.
 *
 * Exported for the same reason isSessionEnded is: it is the decision, and the
 * one place the retry rule can be asserted directly.
 */
export const elevationInterceptor: Interceptor = next => async (req) => {
  try {
    return await next(req)
  }
  catch (err) {
    if (req.stream || !isElevationRequired(err))
      throw err
    if (!(await promptForElevation()))
      throw err
    const retry = restartDeadline(req.signal, req.header)
    try {
      return await next({ ...req, signal: retry.signal })
    }
    finally {
      retry.cleanup()
    }
  }
}

/** The header Connect puts a per-call deadline in. Mirrors `headerTimeout`. */
const CONNECT_TIMEOUT_HEADER = 'connect-timeout-ms'

/**
 * A FRESH deadline for the retry, and the reason it is not optional.
 *
 * Connect mints one deadline signal per call, before any interceptor runs, and
 * hands the same signal to every attempt. The step-up prompt sits INSIDE the
 * refused call — that is what makes the retry automatic — so the seconds the
 * user spends reading the dialog and typing a password are charged to the
 * request's own deadline. Past it the signal aborts while the dialog is still
 * open, and the retry fails instantly with DeadlineExceeded: adding a passkey
 * failed for anyone who did not type fast enough, and the reported cause named
 * a timeout that nothing had actually waited for.
 *
 * Thinking time is not request time. The retry therefore starts its budget
 * over, from the same value the call declared — `connect-timeout-ms` is the
 * per-call deadline Connect already wrote into the request header, so a caller
 * that asked for a longer or shorter one keeps it, and a call with no deadline
 * gets none here either.
 *
 * A CANCELLATION still propagates. The original signal is linked in, minus the
 * one reason this function exists to replace: an abort whose reason is the
 * expired deadline is ignored, and every other reason (a caller's own
 * AbortController) aborts the retry.
 */
function restartDeadline(original: AbortSignal, header: Headers): { signal: AbortSignal, cleanup: () => void } {
  const controller = new AbortController()

  const forward = () => {
    if (!isDeadlineAbort(original))
      controller.abort(original.reason)
  }
  if (original.aborted)
    forward()
  else
    original.addEventListener('abort', forward)

  // An ABSENT or unreadable budget means no deadline, never an expired one.
  // `Number('')` is 0, and a 0 here would abort the retry before it left --
  // the opposite of what "the call declared no deadline" asks for.
  const declared = header.get(CONNECT_TIMEOUT_HEADER)?.trim()
  const timeoutMs = declared ? Number(declared) : Number.NaN
  // Mirrors Connect's own createDeadlineSignal: a non-positive budget is
  // already spent, and the abort reason is the ConnectError the transport
  // would have raised, so a genuinely slow retry still reports itself the
  // way every other timed-out call does.
  const expire = () => controller.abort(new ConnectError('the operation timed out', Code.DeadlineExceeded))
  let timer: ReturnType<typeof setTimeout> | undefined
  if (Number.isFinite(timeoutMs)) {
    if (timeoutMs <= 0)
      expire()
    else
      timer = setTimeout(expire, timeoutMs)
  }

  return {
    signal: controller.signal,
    cleanup: () => {
      clearTimeout(timer)
      original.removeEventListener('abort', forward)
    },
  }
}

/** Whether this signal aborted because its deadline expired. */
function isDeadlineAbort(signal: AbortSignal): boolean {
  return signal.aborted
    && signal.reason instanceof ConnectError
    && signal.reason.code === Code.DeadlineExceeded
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
  // The elevation interceptor sits INSIDE the error interceptor, so a retry
  // that itself fails still reaches the session-ended rule. Neither can fire
  // for the other's case: an elevation refusal is FailedPrecondition and a
  // dead session is Unauthenticated.
  interceptors: [errorInterceptor, elevationInterceptor],
  fetch: getTransportFetch(),
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
  // NO elevation interceptor. The page is going away, so there is nobody to
  // answer a prompt and nothing left to render one in; an op enqueued at
  // unload is not a sensitive mutation either.
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
