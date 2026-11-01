import type { ParentComponent } from 'solid-js'
import type { SignInRecovery } from '~/context/AuthContext'
import type { PostAuthDestination } from '~/lib/postAuthNavigate'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createSignal, on, Show, untrack } from 'solid-js'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { postAuthDestination } from '~/lib/postAuthNavigate'
import { centeredFull } from '~/styles/shared.css'

/** Where a recovered sign-in goes when the account must verify an address. */
const VERIFY_EMAIL_PATH = '/verify-email'

/**
 * Where a completed recovery navigates: `/verify-email` when the sign-in
 * reported that the account must verify an address, and otherwise the
 * address the sign-in was asked to continue to, as `postAuthNavigate` reads
 * it -- which keeps the CLI consent redirect.
 */
function recoveryDestination(recovery: SignInRecovery): PostAuthDestination {
  if (recovery.verificationRequired)
    return { kind: 'route', path: VERIFY_EMAIL_PATH }
  return postAuthDestination(recovery.redirect, '/')
}

/**
 * Whether the router's location is the address `target` names. The router
 * matches `/a/` as `/a`, so a trailing slash does not make two addresses.
 */
function isAtAddress(pathname: string, search: string, target: string): boolean {
  const url = new URL(target, 'http://leapmux.invalid')
  const trim = (path: string) => path.replace(/\/+$/, '') || '/'
  return trim(url.pathname) === trim(pathname) && url.search === search
}

/**
 * Replaces EVERY route while sign-in recovery runs: a sign-in that succeeded
 * but could not report its account, so the session cookie belongs to an
 * account this page has not read yet.
 *
 * It is the OUTERMOST route gate -- it wraps `SetupGate` in `~/app.tsx` -- and
 * that placement is the point. While recovery is set it renders its own
 * screen and NOT its children, so no route component, no `SignedOutOnly` and
 * no `SetupGate` navigator is mounted. Nothing else can navigate, and the
 * source page cannot submit its sign-in a second time.
 *
 * Recovery owns the ONE navigation that follows, in this order:
 *
 * 1. The context adopts the account it read and marks recovery completed.
 * 2. This gate navigates to the destination. A destination the hub serves is
 *    a full-document load, and the document leaves, so recovery is never
 *    cleared on that path.
 * 3. For a client-side route, this gate clears recovery once the router's
 *    location IS the destination. The children then mount directly on the
 *    destination route, and the source route never remounts.
 */
export const SignInRecoveryGate: ParentComponent = (props) => {
  const auth = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  // The client-side destination this gate navigated to, until the router
  // reaches it.
  const [arriving, setArriving] = createSignal<string | null>(null)

  createEffect(on(() => auth.signInRecovery()?.completed ?? false, (completed) => {
    const recovery = untrack(auth.signInRecovery)
    if (!completed || !recovery)
      return
    const destination = recoveryDestination(recovery)
    if (destination.kind === 'document') {
      window.location.assign(destination.path)
      return
    }
    setArriving(destination.path)
    navigate(destination.path, { replace: true })
  }))

  createEffect(() => {
    const target = arriving()
    if (target === null || !isAtAddress(location.pathname, location.search, target))
      return
    setArriving(null)
    auth.finishSignInRecovery()
  })

  return (
    <Show when={auth.signInRecovery()} fallback={props.children}>
      {recovery => (
        <div class={centeredFull}>
          <Show
            when={recovery().error}
            fallback={(
              <div role="status" data-testid="signin-recovery-pending">
                <Spinner size="md" />
                <p>Loading your account…</p>
              </div>
            )}
          >
            {error => (
              <div role="alert" data-testid="signin-recovery">
                <p>You are signed in, but your account could not be loaded.</p>
                <p>{error()}</p>
                <button type="button" onClick={() => void auth.retrySignInRecovery()}>
                  Retry
                </button>
              </div>
            )}
          </Show>
        </div>
      )}
    </Show>
  )
}
