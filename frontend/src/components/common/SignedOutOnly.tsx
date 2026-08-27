import type { ParentComponent } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createEffect, createSignal, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { postAuthNavigate } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'
import { isSoloMode } from '~/lib/systemInfo'
import { centeredFull, pageCard } from '~/styles/shared.css'

/**
 * Sole gate on the credential pages: `/login`, `/signup`, `/forgot-password`,
 * `/reset-password` and `/setup`. It is the mirror of AuthGuard, which guards
 * the authenticated app, and it exists because the credential pages had NO
 * such gate at all.
 *
 * TWO rules, and both belong to every one of the five pages.
 *
 * A SOLO hub serves no credential endpoint at all: it authenticates every
 * request as the synthetic solo user, so there is nothing to sign in to, no
 * account to create, and no password to reset. Each page used to spell that
 * rule out, and it reached two of the five, so `/forgot-password`,
 * `/reset-password` and `/setup` each offered a form the hub answers nothing
 * for. It OUTRANKS `whenSignedIn`: an `explain` panel that offers to sign out
 * is the wrong answer on a hub where signing out is impossible.
 *
 * `SetupGate` does not cover this. It asks whether the HUB still needs a first
 * administrator, which is a different question with a different answer.
 *
 * The second rule is the signed-in visitor, who got the whole form on every one
 * of them. Two of those were more than untidy: `/signup` consulted the hub's
 * signup setting and nothing else, so a signed-in user could create a SECOND
 * account and the page then swapped their session to it without a word, and
 * `/reset-password?token=...` spent the token and rotated a password with no
 * notion of who was signed in. The remedy a signed-in user actually wants for
 * all of them is Preferences -> Account.
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
 * `replace` also takes the tokened address out of that tab's history. This
 * gate must tell that user, and give them the one control that helps.
 */
export const SignedOutOnly: ParentComponent<{
  /**
   * How this gate answers a visitor who ARRIVED signed in.
   *
   * `redirect` (the default) sends them to `?redirect=` or to the app.
   * `explain` renders the panel below instead, stating the conflict and
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
  // document that already started to leave cannot be called back.
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
    // DECIDED ONCE, and the early return is what makes that true. This effect
    // reads the latch and then writes it, and a user effect runs inside
    // `runUpdates`, so the self-write re-queues the effect rather than being
    // dropped. Without the return, the second run finds the latch already set,
    // skips the write, and falls through to the navigation a second time.
    if (arrivedSignedIn() !== null)
      return
    const signedIn = auth.user() !== null
    setArrivedSignedIn(signedIn)
    // The solo rule first: it holds whether or not somebody is signed in, and
    // it outranks `whenSignedIn`, because a hub with no sign-out has nothing
    // to explain.
    if (isSoloMode()) {
      navigate('/', { replace: true })
      return
    }
    if (!signedIn || props.whenSignedIn === 'explain')
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
  // spend a token -- and a visitor who signs in ON the page is finished with
  // it, so hiding it is right there too.
  return (
    <Show
      when={!auth.user()}
      fallback={(
        // The LATCH, not the live signal, for the reason the effect uses it:
        // this panel answers a visitor who ARRIVED signed in. A sign-in
        // performed on the page would otherwise show "your link is still
        // unused" after they finished with it.
        <Show when={props.whenSignedIn === 'explain' && arrivedSignedIn() && !isSoloMode()}>
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
