import type { DesktopBehavior } from '~/api/platformBridge'
import { createEffect, onCleanup } from 'solid-js'
import { parseDesktopBehaviorRefusals, setDesktopBehavior } from '~/api/platformBridge'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { trailingDebounce } from '~/lib/debounce'
import { reportDesktopShellRefusals } from '~/lib/desktopShellStatus'
import { createLogger } from '~/lib/logger'
import { isDesktopApp } from '~/lib/systemInfo'

const log = createLogger('desktopWindowBehavior')

/**
 * How long to wait before pushing, so a burst of edits is one invoke.
 *
 * `reload()` applies one settings load inside a `batch`, so that route already
 * wakes this effect once. What remains is a person: a user who moves two
 * Desktop controls in the same second should not register the login item and
 * rebuild the tray twice.
 *
 * The window is also what makes the `pending` rule below load-bearing, because
 * it is the interval in which a change can be undone before it is sent.
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

  // The key of the payload the SHELL HOLDS. It moves only once an invoke
  // settles in a way that proves the command RAN: a resolve, or a refusal,
  // which the shell reports after it applied and cached everything else. A
  // transport failure leaves it where it was, so the next run of the effect
  // pushes again -- the shell's own cache mirror is written on the same rule,
  // and the two converge only when both obey it.
  let lastPushed: string | undefined
  // The newest payload the effect produced, and the key that identifies it.
  // `trailingDebounce` takes no arguments, and a trailing debounce should send
  // the LAST value anyway, so the pending payload lives here rather than being
  // captured per call.
  //
  // The payload is the TYPED object, and the key is a separate string. Passing
  // a parsed JSON string to the invoke instead would type as `any`, which is
  // the one place a field renamed in `DesktopBehavior` still compiles.
  let pending: DesktopBehavior | undefined
  let pendingKey: string | undefined

  // The newest push this hook issued. The debounce collapses a burst, but two
  // pushes further apart than it can still overlap, and a SUPERSEDED answer
  // must not reach the row: the message would then sit beside a control the
  // user already repaired and read as the repair having failed too.
  let pushSeq = 0

  const push = trailingDebounce(() => {
    const behavior = pending
    const key = pendingKey
    if (behavior === undefined || key === undefined || key === lastPushed)
      return
    const seq = ++pushSeq
    const isNewest = () => seq === pushSeq
    setDesktopBehavior(behavior)
      .then(() => {
        lastPushed = key
        if (isNewest())
          reportDesktopShellRefusals([])
      })
      .catch((err: unknown) => {
        // The shell reports what the operating system refused. The preference
        // is stored either way, so this is a notice and not a rollback -- but
        // it must be a notice the user READS. A tray that could not be created
        // and a login item the system declined both leave a toggle reading
        // "on" with nothing behind it, so each message goes to the row that
        // owns its choice. Anything else (the IPC layer itself failing)
        // belongs to no row and stays in the log.
        const refusals = parseDesktopBehaviorRefusals(err)
        // A REFUSAL means the command ran: it applies every step and caches
        // the set whatever the operating system declined, so the shell holds
        // this payload. Anything else may never have reached the command, so
        // the key stays put and the next run of the effect tries again.
        if (refusals.length > 0)
          lastPushed = key
        log.warn('the desktop shell refused the window behaviour', err)
        if (isNewest())
          reportDesktopShellRefusals(refusals)
      })
  }, PUSH_DEBOUNCE_MS)
  onCleanup(() => push.cancel())

  createEffect(() => {
    // `accountDescriptors` is empty until a `listUserSettings` reply lands, so
    // it is the signal for "the account tier has answered" -- the same one the
    // settings dialog uses to decide whether account rows exist yet.
    if (auth.loading() || auth.user() === null || preferences.accountDescriptors().length === 0)
      return

    const behavior: DesktopBehavior = {
      trayEnabled: preferences.trayEnabled(),
      trayOnClose: preferences.trayOnClose(),
      trayOnMinimize: preferences.trayOnMinimize(),
      startOnLogin: preferences.startOnLogin(),
      startMinimized: preferences.startMinimized(),
    }
    // BOTH are written on every run, including one whose key matches
    // `lastPushed`. Returning early here instead would leave an already-armed
    // timer holding the payload the user moved away from, and it would fire
    // with that superseded value: turn the tray on and off again inside the
    // debounce window, and the shell receives "on". `push` makes the
    // no-change decision instead, where the timer cannot outrun it.
    pending = behavior
    pendingKey = JSON.stringify(behavior)
    push()
  })
}
