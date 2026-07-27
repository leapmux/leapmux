import { AuthGuard } from '~/components/common/AuthGuard'
import { AppShell } from '~/components/shell/AppShell'
import { WorkspaceProvider } from '~/context/WorkspaceContext'

/**
 * Authenticated app shell. Wraps `/` and `/workspace/:workspaceId` so
 * AppShell stays mounted across workspace navigation.
 *
 * This layout deliberately renders NO route outlet: AppShell owns the entire
 * authenticated UI, and both leaves under `(app)/` return null, existing only
 * to give the router a match for their path. Not passing `props.children` is
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
