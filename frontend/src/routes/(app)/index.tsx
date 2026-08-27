/**
 * App home (`/`), and the only route under `(app)`. Renders nothing itself: the
 * `(app).tsx` layout wraps it in `<AuthGuard>` and mounts AppShell, which owns
 * the whole authenticated UI. This file exists only to give the router a leaf
 * for the path.
 *
 * There is deliberately no login redirect here. AuthGuard renders its children
 * only when `!loading() && isAuthenticated()`, so any redirect written in this
 * component would sit behind a condition that is already true and could never
 * run. Unauthenticated routing is AuthGuard's job, and the first-run redirect
 * is SetupGate's, above the router outlet.
 */
export default function IndexRoute() {
  return null
}
