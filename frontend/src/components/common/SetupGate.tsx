import type { ParentComponent } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createMemo, Show } from 'solid-js'
import { BootSplash } from '~/components/common/BootSplash'
import { useAuth } from '~/context/AuthContext'
import { shallowEqual } from '~/lib/shallowEqual'
import { isSetupRequired, isSystemInfoLoaded } from '~/lib/systemInfo'

/** The one address a hub without an account can serve. */
const SETUP_PATH = '/setup'

/** Where a visitor goes when the hub is set up and they asked for `/setup`. */
const AFTER_SETUP_PATH = '/login'

/**
 * What the gate decided to do with one visit.
 *
 * `wait` is neither of the other two: the route must not render, and there is
 * nowhere to send the visitor yet. It holds `/setup` behind the splash until
 * the hub answers.
 */
type SetupDecision
  = | { kind: 'render' }
    | { kind: 'wait' }
    | { kind: 'redirect', to: string }

/**
 * `path` with any trailing slash removed, so one address is one string.
 *
 * The router and `useLocation` disagree here, and an exact compare picks the
 * wrong side. `<Route path="/setup">` MATCHES `/setup/` -- the matcher trims
 * the slash -- while `location.pathname` reports `/setup/` verbatim. So a hub
 * that is already set up served the first-run form to anyone who typed the
 * slash, because this gate saw an address that was not `/setup` and let it
 * through.
 */
function withoutTrailingSlash(path: string): string {
  return path.replace(/\/+$/, '')
}

/**
 * The sole owner of the rule "a hub with no account has exactly ONE usable
 * page", and its mirror, "a hub WITH an account has no first-run page".
 *
 * A non-solo hub whose initial setup is not complete has no account at all.
 * Every credential page therefore offers a form that cannot succeed:
 * `/login` has nothing to sign in to, `/signup` and `/forgot-password` and
 * `/reset-password` have no account to act on, `/verify-email` and
 * `/elevate` need a session nobody can hold. Only `/setup` does anything.
 *
 * It is mounted ONCE, around the router outlet in `~/app.tsx`, and that
 * placement is the point. Each page used to spell the rule out -- AuthGuard,
 * LoginPage and ElevatePage each carried a copy, and the four pages that
 * needed it most carried none -- so which addresses were covered depended on
 * who remembered. Above the outlet, a route added later is covered on the day
 * it is added rather than on the day somebody notices.
 *
 * THREE outcomes, not two: render the route, redirect, or WAIT behind the
 * splash. The third exists for `/setup` alone, and the reason is what that one
 * page renders while nobody knows the answer.
 *
 * Two states leave the answer unknown -- the bootstrap is in flight, and the
 * system info never arrived because the hub is unreachable. In both, the
 * getters answer fabricated defaults, `setupRequired = false` among them.
 *
 * On the four credential pages the unknown answer costs nothing, so they
 * render. The common visitor there is signed out on a hub that is set up, and
 * holding the form behind a splash would delay every sign-in to answer a
 * question that concerns one installation once. This is the same trade
 * `SignedOutOnly` makes.
 *
 * On `/setup` it costs a wrong account. That page paints "Welcome to LeapMux
 * -- Create the first administrator account" over a live sign-up form, and a
 * submit inside that window creates an ORDINARY account under a heading that
 * promised an administrator. So `/setup` waits, and it waits indefinitely
 * while the hub is unreachable. That is deliberate: `AuthGuard` renders the
 * real diagnosis for `/`, and a splash the operator can leave is better than a
 * form that answers them with the wrong kind of account.
 *
 * One state decides to RENDER whatever the snapshot says: somebody is signed
 * in. A session PROVES an account exists, so a snapshot that still says
 * `setupRequired` is stale rather than informative. This is what carries the
 * new administrator home: `/setup` adopts the session and navigates to `/`
 * while its forced re-fetch of the system info is still in flight.
 *
 * Every page below this gate carries a navigation of its own -- AuthGuard
 * sends an unauthenticated visitor to `/login`, SignedOutOnly sends a solo one
 * to `/` -- and they all fire on the same signal this memo reads. Two
 * independent layers keep that from becoming a race:
 *
 *   - The `Show` below is a render effect, so a decision to redirect tears
 *     the subtree down in the update phase, and Solid skips a queued user
 *     effect whose owner is already disposed. The page's navigation is not
 *     ordered after the gate's; it does not happen.
 *   - Anything that still reaches the router -- a navigation from a timer, a
 *     socket callback, the address bar -- meets this decision again, because it
 *     refuses EVERY address but `/setup` while an account is missing. The
 *     state is a fixed point, so the worst such a navigator costs is one
 *     extra replaced entry, never a page that stays.
 */
export const SetupGate: ParentComponent = (props) => {
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  /**
   * What to do with this visit.
   *
   * COMPARED BY VALUE. The memo builds a fresh object on every recompute, and
   * `createMemo` compares with `===` by default, so every recompute would
   * notify the effect below and navigate again to an address the browser is
   * already going to.
   */
  const decision = createMemo<SetupDecision>(() => {
    const onSetupPage = withoutTrailingSlash(location.pathname) === SETUP_PATH
    // A session outranks the snapshot: it proves an account exists.
    if (auth.isAuthenticated())
      return { kind: 'render' }
    if (auth.loading() || !isSystemInfoLoaded())
      return onSetupPage ? { kind: 'wait' } : { kind: 'render' }
    if (isSetupRequired())
      return onSetupPage ? { kind: 'render' } : { kind: 'redirect', to: SETUP_PATH }
    return onSetupPage ? { kind: 'redirect', to: AFTER_SETUP_PATH } : { kind: 'render' }
  }, { kind: 'render' }, { equals: shallowEqual })

  createEffect(() => {
    const current = decision()
    if (current.kind === 'redirect')
      navigate(current.to, { replace: true })
  })

  // This hides the route for the frame between the decision and the landing,
  // for the reason SignedOutOnly states: a form that the app is about to
  // remove can still take a password.
  return (
    <Show when={decision().kind === 'render'} fallback={<BootSplash />}>
      {props.children}
    </Show>
  )
}
