import type { Navigator } from '@solidjs/router'
import { safeRedirect } from '~/lib/safeRedirect'

/**
 * Every route file this SPA carries, as a path relative to this module.
 *
 * `import.meta.glob` resolves at BUILD time and loads nothing: the value is a
 * record of path to a lazy import, and this module reads only the keys. So this
 * is the route tree itself rather than a copy of it, and this module covers a
 * route added under `src/routes/` on the day the file lands.
 */
const ROUTE_FILES = Object.keys(import.meta.glob('../routes/**/*.tsx'))

/** A test file beside a route. It serves no address. */
const TEST_FILE = /\.(?:test|spec)\.tsx?$/

/** A route GROUP -- `(app)` -- which organizes files and adds no segment. */
const ROUTE_GROUP = /^\(.*\)$/

/** A dynamic segment: `[id]` matches one segment, `[...rest]` matches the rest. */
const DYNAMIC_SEGMENT = /^\[.*\]$/
const CATCH_ALL_SEGMENT = /^\[\.\.\..*\]$/

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * The addresses this SPA answers, as patterns.
 *
 * This function leaves the CATCH-ALL out on purpose. `src/routes/[...404].tsx` matches every
 * address, so counting it would make every target look client-side -- and that
 * route IS the 404 page, which is the outcome this module exists to prevent.
 */
function spaRoutePatterns(files: readonly string[]): RegExp[] {
  const patterns: RegExp[] = []
  for (const file of files) {
    if (TEST_FILE.test(file))
      continue
    const segments = file
      .replace(/^.*\/routes\//, '')
      .replace(/\.tsx?$/, '')
      .split('/')
      .filter(segment => !ROUTE_GROUP.test(segment))
    if (segments.some(segment => CATCH_ALL_SEGMENT.test(segment)))
      continue
    // `index` specifies its parent directory's address, not a segment of its own.
    if (segments.at(-1) === 'index')
      segments.pop()
    const body = segments
      .map(segment => (DYNAMIC_SEGMENT.test(segment) ? '[^/]+' : escapeRegExp(segment)))
      .join('/')
    patterns.push(new RegExp(`^/${body}$`))
  }
  return patterns
}

const SPA_ROUTE_PATTERNS = spaRoutePatterns(ROUTE_FILES)

/**
 * The path of a target, without the query, the fragment or a trailing slash.
 *
 * The router trims a trailing slash before it matches, so `/login/` and
 * `/login` are one address to it and must be one address here.
 */
function pathOf(target: string): string {
  const path = target.split(/[?#]/, 1)[0] ?? ''
  const trimmed = path.replace(/\/+$/, '')
  return trimmed === '' ? '/' : trimmed
}

/**
 * Whether a redirect target belongs to the hub rather than to this SPA.
 *
 * INVERTED, and that is the whole point. It used to hold a hand-written list
 * of the hub's prefixes, which held `/auth/` alone while the Go mux also serves
 * `/ws/channel`, `/ws/userevents`, `/metrics`, `/version`,
 * `/worker/delegation-tokens/*` and every Connect RPC path. Each of those took
 * the client-side branch and rendered the SPA's own 404 page -- the exact
 * failure this module exists to prevent -- and nothing linked the list to the
 * mux, so it could only drift.
 *
 * Asking the question the other way round needs no list at all: this
 * application knows its OWN routes, and everything else on the origin belongs
 * to whatever serves it. A route added on either side stays correct with no
 * edit here.
 */
export function isServerRoute(target: string): boolean {
  // A SAME-ORIGIN ABSOLUTE PATH, or nothing. An absolute URL and a
  // protocol-relative `//host/...` both leave this origin, so no route table on
  // either side answers for them -- and claiming one would turn the
  // full-document branch into an off-origin navigation for a caller that did
  // not filter its input first. `postAuthNavigate` filters through
  // `safeRedirect` before it asks; this keeps every other caller safe too.
  if (!target.startsWith('/') || target.startsWith('//'))
    return false
  const path = pathOf(target)
  return !SPA_ROUTE_PATTERNS.some(pattern => pattern.test(path))
}

/**
 * Navigate after a successful sign-in or elevation.
 *
 * The router's `navigate()` is a CLIENT-side transition: it looks the target up
 * in this application's route table. That is right for `/` or `/verify-email`,
 * and wrong for `/oauth/authorize`, which the Go mux serves -- the router finds
 * no entry and renders the 404 page while the CLI waits for a consent screen
 * the user never sees. A full-document assign hands the address back to the
 * server, which is the only thing that can answer it.
 *
 * Every target passes through `safeRedirect` first, so `safeRedirect` drops a
 * value that could leave the origin and this function uses the caller's
 * fallback instead. That one guard covers both branches, deliberately: the
 * full-document branch is the more dangerous sink, and giving it its own copy
 * of the rule is how the two drift apart.
 */
export function postAuthNavigate(
  navigate: Navigator,
  target: string | undefined,
  fallback: string,
): void {
  const safe = safeRedirect(target) ?? fallback
  if (isServerRoute(safe)) {
    window.location.assign(safe)
    return
  }
  navigate(safe, { replace: true })
}
