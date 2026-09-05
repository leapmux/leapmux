import type { ParentComponent } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createMemo, Match, Switch } from 'solid-js'
import { BootSplash } from '~/components/common/BootSplash'
import { PasswordSetupGate } from '~/components/common/PasswordSetupGate'
import { useAuth } from '~/context/AuthContext'
import { assertNever } from '~/lib/assertNever'
import { isAutoAuthenticated, isPasswordSetupGate } from '~/lib/systemInfo'
import { centeredFull } from '~/styles/shared.css'

/**
 * The one decision this guard makes, in the one shape both halves read.
 *
 * Redirecting and rendering are two views of the SAME state, and keeping them
 * as two independent condition ladders is what stranded a solo visitor on a
 * permanent spinner: the effect correctly declined to redirect, the render's
 * conditions had no branch for that case, and neither side could see the gap.
 *
 * The union alone does not deliver that guarantee -- a `<Switch fallback>`
 * would absorb a newly added `kind` and render the spinner, reproducing the
 * original symptom. The `branch` memo below closes it: it switches over every
 * discriminant and ends in `assertNever`, so adding a member here without
 * deciding how it renders is a COMPILE error rather than a silent spinner.
 */
type GuardState
  = | { kind: 'loading' }
    | { kind: 'authenticated' }
    | { kind: 'failed', title: string, detail: string }
    | { kind: 'redirect', to: string }
    /**
     * A TCP connection to a solo hub whose account holds no password. The
     * caller has no session and reaches exactly one protected action: the
     * public first-password procedure. The gate replaces the app with that
     * one form, because every other affordance would refuse this caller.
     */
    | { kind: 'password-setup' }

/**
 * Sole gate on the authenticated app (`(app).tsx`). Everything behind it — `/`,
 * which is the group's only route — decides "who may see this page" here and
 * nowhere else, so no guarded route needs a redirect effect of its own.
 *
 * It never sees a hub whose first-run setup is incomplete: `SetupGate` sits
 * above the router outlet and refuses every address but `/setup` while the
 * hub has no account, so an unauthenticated visitor reaching this memo is on
 * a hub that has one. That rule lives there rather than here because four
 * routes outside this guard need it too.
 *
 * `isAutoAuthenticated()` derives from the systemInfo snapshot signal: it is a safe
 * read because `AuthProvider` awaits `loadSystemInfo()` before it clears
 * `loading()` — by the time `state` is computed past its first line it has
 * its real value, and a later forced refresh re-evaluates this memo
 * reactively. And if that load FAILED it never got a real value at all,
 * which is why the `bootstrapError()` branch sits above every read of it: the
 * guard shows the panel instead of a decision made on fabricated defaults.
 */
export const AuthGuard: ParentComponent = (props) => {
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  const state = createMemo<GuardState>(() => {
    if (auth.loading())
      return { kind: 'loading' }

    // A failed bootstrap is not a missing session: no redirect can fix an
    // unreachable hub, and sending the visitor to /login would hide the cause
    // behind a form that cannot succeed.
    const bootstrapFailure = auth.bootstrapError()
    if (bootstrapFailure) {
      return {
        kind: 'failed',
        title: 'Could not reach the hub.',
        detail: bootstrapFailure,
      }
    }

    // Before the authenticated branch, and before the redirect below. This
    // caller holds no session, so the redirect would send it to a login form
    // that no password can satisfy. A caller that DID sign in never reaches
    // this state, because a stored password ends it.
    if (isPasswordSetupGate())
      return { kind: 'password-setup' }

    if (auth.isAuthenticated())
      return { kind: 'authenticated' }

    // A CREDENTIAL-FREE connection has no login to send them to: the hub
    // authenticates it outright, so an unauthenticated answer here is a
    // transport or configuration failure rather than a missing session.
    // `restoreSession` records no bootstrapError for it — Unauthenticated is
    // the ordinary "not logged in yet" reply everywhere else — so this branch
    // is the only thing that stops that reply from becoming a spinner that
    // never resolves.
    //
    // isAutoAuthenticated, not isSoloMode. A solo hub whose account holds a
    // password asks its network callers to sign in, and one of them arriving
    // without a session is the ORDINARY case there, not a fault: it belongs in
    // the redirect below.
    if (isAutoAuthenticated()) {
      return {
        kind: 'failed',
        title: 'The hub reported no session.',
        detail: 'This connection is authenticated by the hub itself, so there is no sign-in to fall back to. The hub may be misconfigured, or a proxy in front of it may strip credentials.',
      }
    }

    const returnTo = location.pathname + location.search
    return { kind: 'redirect', to: `/login?redirect=${encodeURIComponent(returnTo)}` }
  })

  createEffect(() => {
    const current = state()
    if (current.kind === 'redirect')
      navigate(current.to, { replace: true })
  })

  // Narrowed accessor so the failure branch can read title/detail without
  // re-deriving the discriminant inside the JSX.
  const failure = () => {
    const current = state()
    return current.kind === 'failed' ? current : undefined
  }

  // Which branch renders, as a plain string rather than JSX.
  //
  // A memo returning JSX would rebuild `props.children` on every recompute of
  // `state()`, remounting the whole AppShell whenever auth.user() changes
  // identity. A string memo suppresses equal-value propagation, so the branch
  // only changes when the branch actually changes -- and the `default` is where
  // the compiler enforces exhaustiveness.
  const branch = createMemo<'children' | 'panel' | 'spinner' | 'password-setup'>(() => {
    const current = state()
    switch (current.kind) {
      case 'authenticated':
        return 'children'
      case 'failed':
        return 'panel'
      case 'password-setup':
        return 'password-setup'
      // The guard decided a redirect, but the navigation did not land yet, so
      // there is nothing to show but the same spinner as 'loading'.
      case 'loading':
      case 'redirect':
        return 'spinner'
      default:
        return assertNever(current)
    }
  })

  // No `fallback`: this lists every branch, and `assertNever` above guarantees
  // the set is complete.
  return (
    <Switch>
      <Match when={branch() === 'spinner'}>
        <BootSplash />
      </Match>
      <Match when={branch() === 'children'}>
        {props.children}
      </Match>
      <Match when={branch() === 'password-setup'}>
        <PasswordSetupGate />
      </Match>
      <Match when={branch() === 'panel' && failure()}>
        {current => (
          <div class={centeredFull}>
            <div role="alert" data-testid="auth-bootstrap-error">
              <p>{current().title}</p>
              <p>{current().detail}</p>
              <button type="button" onClick={() => void auth.retryBootstrap()}>
                Retry
              </button>
            </div>
          </div>
        )}
      </Match>
    </Switch>
  )
}
