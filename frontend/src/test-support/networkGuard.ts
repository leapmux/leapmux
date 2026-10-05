/**
 * Refuse every request that a dom test sends to the document's own host.
 *
 * jsdom gives each dom test file the origin `http://localhost:3000`, and the
 * app builds each hub URL from `window.location`: the Connect transport, the
 * worker channel socket and the user-events socket. So a request that a test
 * does not mock goes to whatever process listens on port 3000 on this machine
 * while the suite runs. Port 3000 is a common default for development servers,
 * and the suites of other worktrees send their own leaked requests there too.
 *
 * When nothing listens, the connection is refused and the caller takes its
 * failure path. When a server answers HTTP 401, Connect reports
 * `Code.Unauthenticated`, the error interceptor in `~/api/transport` reads that
 * as an ended session, and `AuthContext` signs the test's user out in the
 * middle of the test. Without the guard, most of `AuthContext.test.tsx` and
 * `usePreferencesForIdentity.test.tsx` fail whenever some other process answers
 * on port 3000, and pass when nothing does.
 *
 * The guard makes the refused connection the only possible outcome:
 *
 * - `fetch` rejects with a `TypeError`, as it does for a refused connection.
 *   Connect then reports the same `Code.Unknown`, so each caller takes the
 *   same path as on a machine where nothing listens.
 * - `XMLHttpRequest.prototype.send` throws a `NetworkError` `DOMException`,
 *   which is what a synchronous request does on a network error. Playwright
 *   bundles `source-map-support`, which sends a synchronous request for a
 *   file-system path when it sees `window`. jsdom resolves that path against
 *   the document origin, and the library catches the error.
 * - `new WebSocket` throws. A refused socket would start the product's
 *   reconnect timers, so a test that reaches one must stub `WebSocket`.
 *
 * Other hosts stay reachable. Several `tests/e2e/helpers` tests start a mock
 * server on an ephemeral `127.0.0.1` port and send requests to it. A `data:` or
 * `blob:` URL has no host, so it never matches.
 *
 * The guard assigns the globals directly, not through `vi.stubGlobal`. So a
 * test that stubs `fetch` or `WebSocket` itself replaces the guard, and
 * `vi.unstubAllGlobals()` restores the guard, not the real implementation.
 */
export function installNetworkGuard(): void {
  guardFetch()
  guardXmlHttpRequest()
  guardWebSocket()
}

/** Marks a function that the guard installed, so a second install is a no-op. */
const GUARDED = Symbol.for('leapmux.networkGuard')

interface Guarded { [GUARDED]?: true }

function isGuarded(value: unknown): boolean {
  return typeof value === 'function' && (value as Guarded)[GUARDED] === true
}

/**
 * Marks a function that the guard itself created.
 *
 * Never call this on a Proxy: with no `defineProperty` trap, the property lands
 * on the proxied target, which is the real implementation.
 */
function markGuarded<T extends object>(value: T): T {
  Object.defineProperty(value, GUARDED, { value: true })
  return value
}

/**
 * The URL that `raw` resolves to, when it targets the document's own host.
 *
 * A relative `raw` resolves against the document base URL, as a browser
 * resolves it for all three APIs. The real `fetch` of Node.js resolves against
 * no base and rejects a relative URL. The guard does not depend on that: undici
 * resolves against its global origin when something sets one.
 *
 * The host is compared, not the origin, so `ws://localhost:3000` matches the
 * document at `http://localhost:3000`. The host is read at each call, because a
 * test can move the document with `jsdom.reconfigure`.
 */
function documentHostTarget(raw: string): URL | null {
  const location = globalThis.location
  if (!location?.host)
    return null
  const base = globalThis.document?.baseURI ?? location.href
  if (!URL.canParse(raw, base))
    return null
  const target = new URL(raw, base)
  return target.host === location.host ? target : null
}

function refusal(what: string, target: URL): string {
  return `The network guard for unit tests refused ${what} ${target.href}. `
    + `That URL is on the jsdom document host, and any process on this machine can listen there. `
    + `Mock the client that sends the request, or stub the API. See ~/test-support/networkGuard.`
}

function guardFetch(): void {
  const realFetch = globalThis.fetch
  if (typeof realFetch !== 'function' || isGuarded(realFetch))
    return
  const guardedFetch: typeof fetch = (input, init) => {
    const request = typeof input === 'string' || input instanceof URL ? undefined : input
    const raw = request ? request.url : String(input)
    const target = documentHostTarget(raw)
    if (!target)
      return realFetch(input, init)
    const method = (init?.method ?? request?.method ?? 'GET').toUpperCase()
    return Promise.reject(new TypeError(refusal(method, target)))
  }
  globalThis.fetch = markGuarded(guardedFetch)
}

function guardXmlHttpRequest(): void {
  if (typeof XMLHttpRequest === 'undefined')
    return
  const prototype = XMLHttpRequest.prototype
  if (isGuarded(prototype.open))
    return
  const realOpen = prototype.open as (this: XMLHttpRequest, ...args: unknown[]) => void
  const realSend = prototype.send
  // The target of the latest `open` on each request. `open` may run again on
  // the same request, so each call replaces the entry.
  const refusedTargets = new WeakMap<XMLHttpRequest, URL>()

  const guardedOpen = function open(this: XMLHttpRequest, ...args: unknown[]): void {
    const target = documentHostTarget(String(args[1]))
    if (target)
      refusedTargets.set(this, target)
    else
      refusedTargets.delete(this)
    realOpen.apply(this, args)
  }
  // An asynchronous request throws here too, where a browser fires `error`.
  // A wrapper cannot emit that failure faithfully: jsdom keeps the ready state
  // and the response in internal fields that only its own network code sets.
  const guardedSend = function send(this: XMLHttpRequest, body?: Document | XMLHttpRequestBodyInit | null): void {
    const target = refusedTargets.get(this)
    if (target)
      throw new DOMException(refusal('the XMLHttpRequest to', target), 'NetworkError')
    realSend.call(this, body)
  }
  prototype.open = markGuarded(guardedOpen) as typeof prototype.open
  prototype.send = guardedSend
}

function guardWebSocket(): void {
  const RealWebSocket = globalThis.WebSocket
  if (typeof RealWebSocket !== 'function' || isGuarded(RealWebSocket))
    return
  // A Proxy keeps the static ready-state constants and `instanceof`, and it
  // adds no class to the prototype chain. It answers the marker from its own
  // `get` trap, because `markGuarded` would write the marker onto the real
  // class, and a later install would then skip the real class.
  globalThis.WebSocket = new Proxy(RealWebSocket, {
    construct(target, args, newTarget) {
      const refused = documentHostTarget(String(args[0]))
      if (refused)
        throw new Error(refusal('the WebSocket to', refused))
      return Reflect.construct(target, args, newTarget)
    },
    get(target, property, receiver) {
      if (property === GUARDED)
        return true
      return Reflect.get(target, property, receiver)
    },
  })
}
