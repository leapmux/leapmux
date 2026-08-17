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
 * Read the account settings again whenever the signed-in identity changes.
 *
 * `PreferencesProvider` loads them ONCE, at its own mount, and it sits above
 * the router and renders unconditionally — so a user who signs in through
 * the login form has already spent that attempt, on a page that had no
 * session, and it answered Unauthenticated. Nothing asked again, so the
 * stored theme, fonts and keybindings never applied for the rest of the
 * session.
 *
 * The trigger cannot live in the provider itself: `useAuth` throws without
 * an `AuthProvider`, and the component tests that render
 * `PreferencesProvider` alone supply none. So it lives HERE, in a hook the
 * one component that already sits inside both providers calls —
 * `PreferencesApplier` in `app.tsx`.
 */
export function useReloadPreferencesOnIdentityChange(): void {
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
    // The FIRST identity the bootstrap resolves is already covered: the
    // provider's own mount load carries the same session cookie, so it read
    // this identity's settings (or failed for a reason a reload here would
    // hit as well). `PreferencesDialog` retries that failure when it opens.
    if (previous === UNRESOLVED)
      return
    // Nothing to read for a signed-out page. The load would answer
    // Unauthenticated and record a load error over a screen that shows no
    // account row. The transition is still RECORDED above, so signing back
    // in as the same user is a change again and does reload.
    if (identity === null)
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
