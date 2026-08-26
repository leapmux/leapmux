import type { Navigator } from '@solidjs/router'
import { safeRedirect } from '~/lib/safeRedirect'

/**
 * Path prefixes served by the hub's Go mux rather than by this SPA.
 *
 * `/auth/` is the whole family: the CLI consent pages (`/auth/cli/start`,
 * `/auth/cli/activate`) and the OAuth legs (`/auth/oauth/...`). None of them
 * is a route in this application, so the router has no entry for them and
 * `navigate()` lands on `[...404]`.
 */
const SERVER_ROUTE_PREFIXES = ['/auth/'] as const

/**
 * Whether a redirect target belongs to the hub rather than to this SPA.
 */
export function isServerRoute(target: string): boolean {
  return SERVER_ROUTE_PREFIXES.some(prefix => target.startsWith(prefix))
}

/**
 * Navigate after a successful sign-in or elevation.
 *
 * The router's `navigate()` is a CLIENT-side transition: it looks the target
 * up in this application's route table. That is right for `/` or
 * `/preferences`, and wrong for `/auth/cli/start`, which the Go mux serves —
 * the router finds no entry and renders the 404 page while the CLI waits for
 * a consent screen the user never sees. A full-document assign hands the
 * address back to the server, which is the only thing that can answer it.
 *
 * Every target passes through `safeRedirect` first, so a value that could
 * leave the origin is dropped and the caller's fallback is used instead.
 * Both branches are covered by that one guard, deliberately: the
 * full-document branch is the more dangerous sink, and giving it its own
 * copy of the rule is how the two drift apart.
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
