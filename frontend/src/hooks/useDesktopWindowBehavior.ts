import { createEffect, onCleanup } from 'solid-js'
import { setDesktopBehavior } from '~/api/platformBridge'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { trailingDebounce } from '~/lib/debounce'
import { createLogger } from '~/lib/logger'
import { isDesktopApp } from '~/lib/systemInfo'

const log = createLogger('desktopWindowBehavior')

/**
 * How long to wait before pushing, so one settings load is one invoke.
 *
 * `reload()` applies the account values one key at a time and un-batched, so a
 * single load wakes this effect once per Desktop key with a partly-updated
 * payload in between. Collapsing them also keeps a transient
 * `startOnLogin: false` from reaching the operating system between the
 * descriptors landing and the values landing.
 */
const PUSH_DEBOUNCE_MS = 50

/**
 * Keep the desktop shell's window behaviour in step with the resolved
 * preferences.
 *
 * The shell cannot read either tier -- one lives in this browser's storage and
 * the other in the account blob -- so the webview is what tells it. Four
 * properties earn their lines:
 *
 * 1. A browser pays nothing: the guard is above the effect, so a web session
 *    creates no subscription at all.
 * 2. Nothing is pushed before the ACCOUNT tier answers. On mount every tier
 *    holds its built-in default, and `start_on_login` has an effect outside the
 *    app -- pushing `startOnLogin: false` on every launch would deregister the
 *    user's login item and register it again a moment later.
 * 3. One invoke per real change, through a payload comparison and a short
 *    debounce. An unrelated preference cannot wake the effect at all, because
 *    it reads only these five accessors.
 * 4. Signing out pushes nothing. The tray icon and the login item belong to the
 *    operating-system user and the machine, not to the browsing session, so
 *    signing out to switch accounts must not make the icon vanish and the login
 *    item deregister.
 */
export function useDesktopWindowBehavior(): void {
  if (!isDesktopApp())
    return

  const auth = useAuth()
  const preferences = usePreferences()
  let lastPushed: string | undefined
  // The newest payload the effect produced. `trailingDebounce` takes no
  // arguments, and a trailing debounce should send the LAST value anyway, so
  // the pending payload lives here rather than being captured per call.
  let pending: string | undefined

  const push = trailingDebounce(() => {
    const payload = pending
    if (payload === undefined || payload === lastPushed)
      return
    lastPushed = payload
    setDesktopBehavior(JSON.parse(payload) as Parameters<typeof setDesktopBehavior>[0])
      .catch((err: unknown) => {
        // The shell reports what the operating system refused. The preference
        // is stored either way, so this is a notice and not a rollback.
        log.warn('the desktop shell refused the window behaviour', err)
      })
  }, PUSH_DEBOUNCE_MS)
  onCleanup(() => push.cancel())

  createEffect(() => {
    // `accountDescriptors` is empty until a `listUserSettings` reply lands, so
    // it is the signal for "the account tier has answered" -- the same one the
    // settings dialog uses to decide whether account rows exist yet.
    if (auth.loading() || auth.user() === null || preferences.accountDescriptors().length === 0)
      return

    const payload = JSON.stringify({
      trayEnabled: preferences.trayEnabled(),
      trayOnClose: preferences.trayOnClose(),
      trayOnMinimize: preferences.trayOnMinimize(),
      startOnLogin: preferences.startOnLogin(),
      startMinimized: preferences.startMinimized(),
    })
    if (payload === lastPushed)
      return
    pending = payload
    push()
  })
}
