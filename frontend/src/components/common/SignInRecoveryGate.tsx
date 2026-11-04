import type { ParentComponent } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createSignal, on, Show, untrack } from 'solid-js'
import { Spinner } from '~/components/common/Spinner'
import { useAuth } from '~/context/AuthContext'
import { followDestination, normalizeRoutePath, postSignInDestination } from '~/lib/postAuthNavigate'
import { centeredFull } from '~/styles/shared.css'

/**
 * Whether the router's location is the address that `target` identifies. The
 * router matches `/a/` as `/a`, so a trailing slash does not make two
 * addresses.
 */
function isAtAddress(pathname: string, search: string, target: string): boolean {
  const url = new URL(target, 'http://leapmux.invalid')
  return normalizeRoutePath(url.pathname) === normalizeRoutePath(pathname) && url.search === search
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
 * The same placement hides the user menu, so a read that keeps failing would
 * hold the visitor on this screen. The failure screen therefore offers Sign
 * out beside Retry: it ends the session, and the context ends recovery with
 * it.
 *
 * Recovery owns the ONE navigation that follows, in this order:
 *
 * 1. The context adopts the account it read and marks the read completed.
 * 2. This gate navigates to the destination that `postSignInDestination`
 *    gives. A destination the hub serves is a full-document load, and the
 *    document leaves, so recovery is never cleared on that path.
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
  const [signingOut, setSigningOut] = createSignal(false)

  createEffect(on(() => auth.signInRecovery()?.read.kind === 'completed', (completed) => {
    const recovery = untrack(auth.signInRecovery)
    if (!completed || !recovery)
      return
    const destination = postSignInDestination(recovery.verificationRequired, recovery.redirect)
    if (destination.kind === 'route')
      setArriving(destination.path)
    followDestination(navigate, destination)
  }))

  createEffect(() => {
    const target = arriving()
    if (target === null || !isAtAddress(location.pathname, location.search, target))
      return
    setArriving(null)
    auth.finishSignInRecovery()
  })

  // The failed read, narrowed so the failure screen reads its message without
  // deriving the discriminant again in the JSX.
  const failure = () => {
    const read = auth.signInRecovery()?.read
    return read?.kind === 'failed'
      ? read
      : undefined
  }

  const signOut = () => {
    setSigningOut(true)
    void (async () => {
      try {
        await auth.logout()
      }
      finally {
        setSigningOut(false)
      }
    })()
  }

  return (
    <Show when={auth.signInRecovery()} fallback={props.children}>
      <div class={centeredFull}>
        <Show
          when={failure()}
          fallback={(
            <div role="status" data-testid="signin-recovery-pending">
              <Spinner size="md" />
              <p>Loading your account…</p>
            </div>
          )}
        >
          {failed => (
            <div role="alert" data-testid="signin-recovery">
              <p>You are signed in, but your account could not be loaded.</p>
              <p>{failed().message}</p>
              <button type="button" disabled={signingOut()} onClick={() => void auth.retrySignInRecovery()}>
                Retry
              </button>
              <button type="button" disabled={signingOut()} onClick={signOut} data-testid="signin-recovery-sign-out">
                {signingOut()
                  ? 'Signing out...'
                  : 'Sign out'}
              </button>
            </div>
          )}
        </Show>
      </div>
    </Show>
  )
}
