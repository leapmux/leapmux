import type { ParentComponent } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createMemo, Show } from 'solid-js'
import { BootSplash } from '~/components/common/BootSplash'
import { useAuth } from '~/context/AuthContext'
import { isSetupRequired, isSystemInfoLoaded } from '~/lib/systemInfo'

/** The one address a hub without an account can serve. */
const SETUP_PATH = '/setup'

/** Where a visitor goes when the hub is set up and they asked for `/setup`. */
const AFTER_SETUP_PATH = '/login'

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
 * placement is the point. The rule used to be spelled per page -- AuthGuard,
 * LoginPage and ElevatePage each carried a copy, and the four pages that
 * needed it most carried none -- so which addresses were covered depended on
 * who remembered. Above the outlet, a route added later is covered on the day
 * it is added rather than on the day somebody notices.
 *
 * Three states decide NOTHING, and each for its own reason:
 *
 *   - The bootstrap is in flight. Children render meanwhile, so the common
 *     visitor is not held behind a splash to answer a question that concerns
 *     one installation once. This is the same trade `SignedOutOnly` makes.
 *   - The system info never arrived (the hub is unreachable). The getters
 *     answer fabricated defaults there, `setupRequired = false` among them,
 *     so deciding on them would bounce the operator off `/setup` -- the one
 *     page they need -- and onto a login form that cannot work either.
 *     AuthGuard renders the real diagnosis for `/`.
 *   - Somebody is signed in. A session PROVES an account exists, so a
 *     snapshot that still says `setupRequired` is stale rather than
 *     informative. This is what carries the new administrator home: `/setup`
 *     adopts the session and navigates to `/` while its forced re-fetch of
 *     the system info is still in flight.
 *
 * Every page below this gate carries a navigation of its own -- AuthGuard
 * sends an unauthenticated visitor to `/login`, LoginPage sends a solo one to
 * `/` -- and they all fire on the same signal this memo reads. Two
 * independent layers keep that from becoming a race:
 *
 *   - The `Show` below is a render effect, so a decision to redirect tears
 *     the subtree down in the update phase, and Solid skips a queued user
 *     effect whose owner is already disposed. The page's navigation is not
 *     ordered after the gate's; it does not happen.
 *   - Anything that still reaches the router -- a navigation from a timer, a
 *     socket callback, the address bar -- is answered again, because this
 *     refuses EVERY address but `/setup` while an account is missing. The
 *     state is a fixed point, so the worst such a navigator costs is one
 *     extra replaced entry, never a page that stays.
 */
export const SetupGate: ParentComponent = (props) => {
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  /** Where this visitor must go instead, or null when the route may render. */
  const redirectTo = createMemo<string | null>(() => {
    if (auth.loading() || !isSystemInfoLoaded() || auth.isAuthenticated())
      return null
    const onSetupPage = withoutTrailingSlash(location.pathname) === SETUP_PATH
    if (isSetupRequired())
      return onSetupPage ? null : SETUP_PATH
    return onSetupPage ? AFTER_SETUP_PATH : null
  })

  createEffect(() => {
    const target = redirectTo()
    if (target !== null)
      navigate(target, { replace: true })
  })

  // The route is hidden for the frame between the decision and the landing,
  // for the reason SignedOutOnly states: a form that is on its way out can
  // still take a password.
  return (
    <Show when={redirectTo() === null} fallback={<BootSplash />}>
      {props.children}
    </Show>
  )
}
