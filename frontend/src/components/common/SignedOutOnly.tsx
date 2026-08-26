import type { ParentComponent } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { centeredFull, pageCard } from '~/styles/shared.css'

/**
 * Sole gate on the credential pages: `/login`, `/signup`, `/forgot-password`,
 * `/reset-password` and `/setup`. It is the mirror of AuthGuard, which gates
 * the authenticated app, and it exists because the credential pages had NO
 * such gate at all.
 *
 * A signed-in visitor got the whole form on every one of them. Two of those
 * were more than untidy: `/signup` gates only on the hub's signup setting, so
 * a signed-in user could create a SECOND account and the page then swapped
 * their session to it without a word, and `/reset-password?token=...` spent
 * the token and rotated a password with no notion of who was signed in. The
 * remedy a signed-in user actually wants for all of them is Preferences ->
 * Account.
 *
 * ONE component rather than an effect in each page, for the reason AuthGuard
 * states for its own half: five copies of a redirect rule drift, and the two
 * pages that needed it most had no copy at all.
 *
 * It honors `?redirect=`, which is not decoration. A CLI sign-in bounces the
 * browser to `/login?redirect=/auth/cli/start...`; a user who was ALREADY
 * signed in had to sign in a second time to reach that consent screen, and
 * sending them to `/` instead would end the CLI flow with nothing on screen.
 * postAuthNavigate is the same call the login form makes on success, so the
 * target passes safeRedirect and a hub-served address gets the full-document
 * assign it needs.
 *
 * `whenSignedIn` picks what a signed-in ARRIVAL gets, and `/reset-password`
 * is the one page that must not take the default. Four of the five carry
 * nothing: the visitor asked for a form, they do not need it, and the app is
 * where they wanted to go -- often through the `?redirect=` they arrived
 * with. A reset link carries a SINGLE-USE token in its address and no
 * redirect, so a silent bounce spends nothing and explains nothing, and the
 * `replace` also takes the tokened address out of that tab's history. That
 * user needs to be told, and given the one control that helps.
 */
export const SignedOutOnly: ParentComponent<{
  /**
   * How a visitor who ARRIVED signed in is answered.
   *
   * `redirect` (the default) sends them to `?redirect=` or to the app.
   * `explain` renders the panel below instead, naming the conflict and
   * offering to sign out.
   */
  whenSignedIn?: 'redirect' | 'explain'
}> = (props) => {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  // Whether this visitor ARRIVED signed in, latched once.
  //
  // The gate acts on the arrival and never on a later change, and the
  // difference is not academic: every page under it signs somebody in, and
  // each does its own navigation afterwards. Reacting to `auth.user()`
  // becoming set put a SECOND navigator on that one state change, and the
  // two do not always agree -- LoginPage and SignupPage send a
  // verification-required account to `/verify-email`, while this sends it to
  // `?redirect=`. Solid flushes this effect before the page's own call, so
  // the page usually wins by navigating last; but a `?redirect=` that points
  // at a hub route takes postAuthNavigate's full-document branch, and a
  // document that is already leaving cannot be called back.
  //
  // A latch removes the race rather than ordering it: a sign-in performed ON
  // the page never reaches this at all.
  const [arrivedSignedIn, setArrivedSignedIn] = createSignal<boolean | null>(null)

  createEffect(() => {
    // While the bootstrap runs, `user()` is null and the children render --
    // which is the RIGHT default here, because the common visitor to these
    // pages is signed out and must not wait behind a splash. The gate decides
    // once the bootstrap resolves.
    if (auth.loading())
      return
    if (arrivedSignedIn() === null)
      setArrivedSignedIn(auth.user() !== null)
    if (!arrivedSignedIn() || props.whenSignedIn === 'explain')
      return
    postAuthNavigate(navigate, stringParam(searchParams.redirect), '/')
  })

  const [signingOut, setSigningOut] = createSignal(false)

  const signOut = () => {
    setSigningOut(true)
    void (async () => {
      try {
        // The latch is NOT re-armed. auth.logout() does not navigate and this
        // route sits outside AuthGuard, so clearing the user re-renders the
        // form at the same address -- with the token still in it, unspent.
        await auth.logout()
      }
      finally {
        setSigningOut(false)
      }
    })()
  }

  // Rendering reads the LIVE signal, not the latch. A visitor who arrived
  // signed in must not see the form for the frame between the redirect and
  // the route change -- a window in which they can still type a password or
  // spend a token -- and a visitor who signs in ON the page has finished with
  // it, so hiding it is right there too.
  return (
    <Show
      when={!auth.user()}
      fallback={(
        // The LATCH, not the live signal, for the reason the effect uses it:
        // this panel answers a visitor who ARRIVED signed in. A sign-in
        // performed on the page would otherwise be greeted with "your link is
        // still unused" after they finished with it.
        <Show when={props.whenSignedIn === 'explain' && arrivedSignedIn()}>
          <div class={centeredFull}>
            <div class={pageCard} data-testid="signed-out-only-explain">
              <h1>You are already signed in</h1>
              <p>
                This link resets the password of the account it was sent to,
                and this browser is signed in as
                {' '}
                <strong>{auth.user()?.username}</strong>
                . Sign out to continue, or close this page to keep the link
                for later — it is still unused.
              </p>
              <button type="button" onClick={signOut} disabled={signingOut()} data-testid="signed-out-only-sign-out">
                {signingOut() ? 'Signing out...' : 'Sign out and continue'}
              </button>
            </div>
          </div>
        </Show>
      )}
    >
      {props.children}
    </Show>
  )
}
