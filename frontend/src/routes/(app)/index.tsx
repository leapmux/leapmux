/**
 * App home (`/`). Renders nothing itself: the `(app).tsx` layout wraps this
 * route in `<AuthGuard>` and mounts AppShell, which owns the whole authenticated
 * UI. Like `(app)/workspace/[workspaceId].tsx`, this file exists only to give
 * the router a leaf for the path.
 *
 * There is deliberately no setup/login redirect here. AuthGuard renders its
 * children only when `!loading() && isAuthenticated()`, so any redirect written
 * in this component would sit behind a condition that is already true and could
 * never run. Unauthenticated routing is AuthGuard's job.
 */
export default function IndexRoute() {
  return null
}
