import { createEffect } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { createLogger } from '~/lib/logger'

const log = createLogger('preferencesIdentity')

/**
 * The sentinel for "the auth bootstrap has not answered yet".
 *
 * A plain `null` cannot carry it: null is also the SIGNED-OUT identity, and
 * the two lead to opposite decisions on the first resolved value.
 */
const UNRESOLVED = Symbol('unresolved identity')

/**
 * Answer the two identity transitions the preferences cannot see for
 * themselves: reload the account settings when the identity CHANGES, and return
 * every tier to its defaults when the identity GOES AWAY.
 *
 * The ACCOUNT tier is reloaded only on a change. The provider's own mount load
 * carries the same session cookie, so on an ordinary page load it already read
 * this identity's settings; asking again would be a second request for the same
 * answer. What it cannot cover is the user who signs in through the login form
 * after that attempt answered Unauthenticated, which is the case this reload
 * exists for.
 *
 * A SIGN-OUT is client-side: the provider and everything under it stay mounted,
 * and both tiers still hold the departing account's values. `resetForSignOut`
 * returns them to the shipped defaults, so the sign-in page does not render in
 * the palette and fonts of whoever used the browser last.
 *
 * SEEDING is not here. The provider subscribes to `onStorageAccountChange`, so
 * an arriving account re-reads the device tier on its own -- synchronously with
 * the identity write, which an effect cannot be.
 *
 * The trigger cannot live in the provider itself: `useAuth` throws without
 * an `AuthProvider`, and the component tests that render
 * `PreferencesProvider` alone supply none. So it lives HERE, in a hook the
 * one component that already sits inside both providers calls —
 * `PreferencesApplier` in `app.tsx`.
 */
export function usePreferencesForIdentity(): void {
  const auth = useAuth()
  const prefs = usePreferences()

  /**
   * The identity whose account settings the context already loaded, or
   * tried to.
   *
   * Per hook instance, never a module-level variable: two mounted trees
   * (a test suite, a re-render after an error boundary reset) each own
   * their own providers and must each decide on their own.
   */
  let loadedFor: string | null | typeof UNRESOLVED = UNRESOLVED

  createEffect(() => {
    // While the bootstrap runs, `user()` is null because the identity is
    // NOT KNOWN, not because the visitor is signed out. Recording that null
    // as an identity would make the restore that follows it look like a
    // sign-in, and every ordinary page load would issue a second load.
    if (auth.loading())
      return
    const identity = auth.user()?.id ?? null
    // The identity is what changes, not the User object. `refreshUser`
    // replaces the object with an equal one on every call, and a reload per
    // refresh is exactly the redundancy this guard exists to stop.
    if (identity === loadedFor)
      return
    const previous = loadedFor
    loadedFor = identity
    // A signed-out page has nothing to LOAD -- the account request would answer
    // Unauthenticated and record an error over a screen that shows no account
    // row -- but it does have something to FORGET. Both tiers still hold the
    // values of whoever just left, and the provider stays mounted, so the
    // sign-in page would render in their palette and fonts. The transition is
    // still RECORDED above, so signing back in as the same user is a change
    // again.
    //
    // `previous === UNRESOLVED` is the ordinary first load of a page nobody is
    // signed in to, where every tier already holds its default. Resetting then
    // is a no-op, and doing it unconditionally keeps one rule rather than two.
    if (identity === null) {
      prefs.resetForSignOut()
      return
    }
    // The FIRST identity the bootstrap resolves is already covered for the
    // ACCOUNT tier: the provider's own mount load carries the same session
    // cookie, so it read this identity's settings (or failed for a reason a
    // reload here would hit as well). `PreferencesDialog` retries that failure
    // when it opens.
    if (previous === UNRESOLVED)
      return
    prefs.reload().catch((err: unknown) => {
      // `reload` records the failure in `accountLoadError`, which the
      // preferences dialog renders and retries. Warn rather than debug:
      // unlike the provider's mount load, this one runs for an identity
      // that IS signed in, so a failure here is not an expected outcome.
      log.warn('account settings reload after an identity change failed', err)
    })
  })
}
