/**
 * URL → cold-start byte bucket for the mobile LTE boot tracer.
 *
 * Kept in `src/` (not only under tests/e2e) so vitest can guard the
 * classification rules next to the code: a wrong bucket silently reorders
 * which critical-path cut the timing report recommends.
 */

export const STARTUP_BUCKETS = [
  'fonts',
  'entry_js',
  'critical_modules',
  'route_app',
  'workers',
  'rpc',
  'other',
] as const

export type StartupBucket = (typeof STARTUP_BUCKETS)[number]

/**
 * Map a request URL onto a startup bucket.
 *
 * Ordering matters: fonts and RPC paths are checked before generic `.js`
 * fallthrough, so a worker or route chunk never lands in `critical_modules`.
 */
export function classifyStartupUrl(url: string): StartupBucket {
  let path: string
  try {
    path = new URL(url).pathname
  }
  catch {
    path = url
  }

  if (path.includes('/fonts/'))
    return 'fonts'

  // Connect-RPC unary paths: /leapmux.v1.AuthService/GetSystemInfo, …
  if (path.includes('/leapmux.v1.'))
    return 'rpc'

  const base = decodeURIComponent(path.split('/').pop() ?? '')

  if (
    (base.startsWith('client-') && base.endsWith('.js'))
    || base.startsWith('rolldown-runtime')
  ) {
    return 'entry_js'
  }

  // Vinxi names the authenticated SPA chunk `(app)-….js` (encoded %28app%29).
  if (base.includes('(app)'))
    return 'route_app'

  // Worker entry chunks are `shikiWorker-<hash>.js` / `markdownWorker-<hash>.js`.
  // The main-thread bridge is `shikiWorkerClient-<hash>.js` and must stay in
  // critical_modules — a `startsWith('shikiWorker')` check would steal it.
  if (
    /^shikiWorker-[\w-]+\.js$/.test(base)
    || /^markdownWorker-[\w-]+\.js$/.test(base)
    || base.startsWith('wasm-')
  ) {
    return 'workers'
  }

  if (base.endsWith('.js') || base.endsWith('.mjs'))
    return 'critical_modules'

  return 'other'
}

/** Empty per-bucket counters. */
export function emptyStartupBucketCounts(): Record<StartupBucket, number> {
  return {
    fonts: 0,
    entry_js: 0,
    critical_modules: 0,
    route_app: 0,
    workers: 0,
    rpc: 0,
    other: 0,
  }
}
