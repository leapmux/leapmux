import type { Component } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, Match, Switch } from 'solid-js'
import { BootSplash } from '~/components/common/BootSplash'
import { ElevateForm } from '~/components/common/ElevateForm'
import { useAuth } from '~/context/AuthContext'
import { assertNever } from '~/lib/assertNever'
import { isElevationCurrent } from '~/lib/elevation'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { safeRedirect } from '~/lib/safeRedirect'
import { stringParam } from '~/lib/searchParam'
import { shallowEqual } from '~/lib/shallowEqual'
import { isAutoAuthenticated } from '~/lib/systemInfo'
import { centeredFull, pageCard } from '~/styles/shared.css'

/**
 * The standalone step-up page.
 *
 * It sits OUTSIDE the `(app)` group, so it renders without the application
 * shell — the hub bounces a CLI login here from `/oauth/authorize`, and that
 * user has no reason to load the workspace. It therefore carries its own
 * guard, modelled on AuthGuard: the same states, decided once, with the
 * redirect and the render reading one value.
 *
 * The FIRST thing it does when the session is already elevated is leave.
 * That is one of the two independent loop-prevention layers the hub's
 * consent gate relies on (the other is the `elevated=1` marker the hub adds
 * to the address it sends the browser back to): without it, a browser that
 * arrives already elevated would sit on a verification prompt for a window
 * it already holds.
 */
type ElevateState
  = | { kind: 'loading' }
    | { kind: 'redirect', to: string }
    | { kind: 'prompt' }

export const ElevatePage: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  /** Where to send the browser once the session is elevated. */
  const returnTo = () =>
    safeRedirect(stringParam(searchParams.redirect)) ?? '/'

  /**
   * This page's own address, with the return target preserved.
   *
   * Two callers need it and both send the browser away and back: the sign-in
   * redirect, which nests it one level deeper, and the OAuth round trip.
   */
  const elevateHere = () => `/elevate?redirect=${encodeURIComponent(returnTo())}`

  /**
   * A factor was proven in THIS document, so the page is finished whatever
   * deadline the response carried.
   *
   * It exists so the state memo is the ONE thing that decides to leave. The
   * ceremony used to navigate directly as well, which meant a successful
   * elevation ran the transition twice: once from the handler and once from
   * the effect below, after the new deadline made the memo recompute. It
   * also covers the shape a deadline cannot: a success whose
   * elevation_expires_at is somehow unset would leave the memo on 'prompt'
   * and strand the user on a form they already answered.
   */
  const [proven, setProven] = createSignal(false)

  const state = createMemo<ElevateState>(() => {
    if (auth.loading())
      return { kind: 'loading' }
    // A CREDENTIAL-FREE connection carries the synthetic solo user, which has
    // no session row to stamp, so there is nothing to elevate. A solo hub
    // reached at a network address is not that connection: it holds a real
    // session and elevates like any other, which is what lets the person who
    // set the password write a hub setting from the browser they set it in.
    //
    // A hub whose first-run setup is incomplete never reaches this memo at
    // all: SetupGate holds every address but `/setup` above the router outlet.
    if (isAutoAuthenticated())
      return { kind: 'redirect', to: '/' }
    if (!auth.isAuthenticated())
      return { kind: 'redirect', to: `/login?redirect=${encodeURIComponent(elevateHere())}` }
    if (proven() || isElevationCurrent(auth.elevationExpiresAt()))
      return { kind: 'redirect', to: returnTo() }
    return { kind: 'prompt' }
    // COMPARED BY VALUE, and the seed is the state the memo answers while the
    // bootstrap runs.
    //
    // `createMemo` compares with `===` by default, and this builds a FRESH
    // object on every recompute, so `{kind:'redirect', to}` never equals the
    // `{kind:'redirect', to}` before it. Each recompute then notified the
    // effect below, and that effect navigates.
    //
    // It is reachable, not theoretical. A successful elevation writes two
    // signals from an async continuation -- the adopted deadline, then `proven`
    // -- and Solid does not batch across an await. So the memo recomputed
    // twice, the effect ran twice, and `postAuthNavigate` fired twice. On the
    // CLI path that is two full-document loads of a single-use consent address.
    //
    // The comparison sits HERE rather than as a `batch` at the write site,
    // because it holds for any future writer. A `batch` has to be remembered at
    // each new one.
  }, { kind: 'loading' }, { equals: shallowEqual })

  createEffect(() => {
    const current = state()
    if (current.kind === 'redirect')
      postAuthNavigate(navigate, current.to, '/')
  })

  // A string memo, so the branch changes only when the branch changes; the
  // `default` is where the compiler enforces exhaustiveness. Same shape as
  // AuthGuard.
  const branch = createMemo<'spinner' | 'prompt'>(() => {
    const current = state()
    switch (current.kind) {
      case 'prompt':
        return 'prompt'
      case 'loading':
      case 'redirect':
        return 'spinner'
      default:
        return assertNever(current)
    }
  })

  return (
    <Switch>
      <Match when={branch() === 'spinner'}>
        <BootSplash />
      </Match>
      <Match when={branch() === 'prompt'}>
        <div class={centeredFull}>
          <div class={pageCard} data-testid="elevate-card">
            <h1>Verify your identity</h1>
            <p>
              This action needs a recent sign-in. Verify below, and the app
              returns you to your previous page.
            </p>
            <ElevateForm
              oauthRedirect={elevateHere()}
              // Record the outcome and let the state memo decide. The effect
              // above is the one navigator, and the memo's own `equals` keeps
              // one decided target to one transition -- the ceremony writes two
              // signals from an async continuation, which Solid does not batch.
              onElevated={() => setProven(true)}
            />
          </div>
        </div>
      </Match>
    </Switch>
  )
}
