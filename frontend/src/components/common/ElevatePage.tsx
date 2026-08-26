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
import { isSoloMode } from '~/lib/systemInfo'
import { centeredFull, pageCard } from '~/styles/shared.css'

/**
 * The standalone step-up page.
 *
 * It sits OUTSIDE the `(app)` group, so it renders without the application
 * shell — the hub bounces a CLI login here from `/auth/cli/start`, and that
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
   * A factor was proven in THIS document, so the page is finished whatever
   * deadline the response carried.
   *
   * It exists so the state memo is the ONE thing that decides to leave. The
   * ceremony used to navigate directly as well, which meant a successful
   * elevation ran the transition twice: once from the handler and once from
   * the effect below, after the new deadline made the memo recompute. It
   * also covers the shape a deadline cannot: a success whose
   * elevation_expires_at is somehow unset would leave the memo on 'prompt'
   * and strand the user on a form they had already answered.
   */
  const [proven, setProven] = createSignal(false)

  const state = createMemo<ElevateState>(() => {
    if (auth.loading())
      return { kind: 'loading' }
    // Solo mode authenticates every request as the synthetic solo user and
    // has no session row to stamp, so there is nothing to elevate. A hub whose
    // first-run setup is incomplete never reaches this memo at all: SetupGate
    // holds every address but `/setup` above the router outlet.
    if (isSoloMode())
      return { kind: 'redirect', to: '/' }
    if (!auth.isAuthenticated())
      return { kind: 'redirect', to: `/login?redirect=${encodeURIComponent(`/elevate?redirect=${encodeURIComponent(returnTo())}`)}` }
    if (proven() || isElevationCurrent(auth.elevationExpiresAt()))
      return { kind: 'redirect', to: returnTo() }
    return { kind: 'prompt' }
  })

  createEffect(() => {
    const current = state()
    if (current.kind === 'redirect')
      postAuthNavigate(navigate, current.to, '/')
  })

  // A string memo, so the arm changes only when the arm changes; the
  // `default` is where exhaustiveness is enforced. Same shape as AuthGuard.
  const arm = createMemo<'spinner' | 'prompt'>(() => {
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
      <Match when={arm() === 'spinner'}>
        <BootSplash />
      </Match>
      <Match when={arm() === 'prompt'}>
        <div class={centeredFull}>
          <div class={pageCard} data-testid="elevate-card">
            <h1>Verify your identity</h1>
            <p>
              This action needs a recent sign-in. Verify below, then you will be
              taken back to what you were doing.
            </p>
            <ElevateForm
              oauthRedirect={`/elevate?redirect=${encodeURIComponent(returnTo())}`}
              // Record the outcome and let the state memo decide. The effect
              // above is the one navigator, so a proven factor cannot start
              // the transition twice.
              onElevated={() => setProven(true)}
            />
          </div>
        </div>
      </Match>
    </Switch>
  )
}
