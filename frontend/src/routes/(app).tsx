import { AuthGuard } from '~/components/common/AuthGuard'
import { AppShell } from '~/components/shell/AppShell'
import { WorkspaceProvider } from '~/context/WorkspaceContext'

/**
 * Authenticated app shell. `/` is the group's only route: there is no
 * per-workspace path, so switching workspaces never remounts anything. Which
 * workspace is active lives in `WorkspaceProvider`'s signal, persisted per user
 * to localStorage by `createWorkspaceSwitcher`.
 *
 * This layout deliberately renders NO route outlet: AppShell owns the entire
 * authenticated UI, and the single leaf under `(app)/` returns null, existing
 * only to give the router a match for the path. Not passing `props.children` is
 * what keeps that arrangement honest — AppShell no longer accepts children, so
 * a new route under `(app)/` that renders real content becomes a type error
 * here rather than a page that mounts and silently shows nothing.
 *
 * To add such a route: give AppShell back a `children` prop and an arm that
 * renders it for non-workspace paths (`hasWorkspaceDesktopChrome` is the
 * predicate that separates them), then pass `props.children` through below. The
 * arm that used to do this wrapped children in a full-window scroll container;
 * a mobile-safe replacement must not let that container become page-scrollable
 * underneath nested mobile UI, which is why iOS needed the guard it carried.
 */
export default function AppLayout() {
  return (
    <AuthGuard>
      <WorkspaceProvider>
        <AppShell />
      </WorkspaceProvider>
    </AuthGuard>
  )
}
