import type { Component } from 'solid-js'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { createSignal, onCleanup } from 'solid-js'
import { DEFAULT_NAV_GROUP_ID } from '~/components/settings/navGroups'

/**
 * The search parameter that carries the Preferences dialog: its presence
 * opens the dialog, and its value is the category id to show (a NAV_GROUPS
 * id, e.g. 'appearance' or 'admin-email').
 *
 * PREFERENCES HAS AN ADDRESS, and the address is the one source of truth for
 * both facts. Three things follow, and a signal supplied none of them:
 *
 *   - The Back button closes the dialog, because opening it pushes a history
 *     entry.
 *   - A pasted link opens the dialog on the section it specifies.
 *   - The OAuth step-up leg has a real target to return the browser to. It is
 *     a full-document navigation out of the app, and it used to come back to
 *     "/" with the panel gone. An account that holds no password and no
 *     passkey elevates ONLY through its identity provider, so for that account
 *     changing the email or detaching a provider could not be completed at
 *     all.
 *
 * A SEARCH PARAMETER rather than a `/preferences/:category` route, because
 * Preferences renders OVER the app rather than in place of it. A route would
 * have to keep the shell mounted underneath and render nothing in the outlet,
 * which states the same arrangement less honestly, and it would take the
 * desktop Apple-menu item away from the page it opens over. A search parameter
 * modifies the current page, which is what the dialog does.
 */
const PREFERENCES_PARAM = 'prefs'

/**
 * The router, as the Preferences state needs it.
 *
 * `openPreferences` is a module-level function because one of its three
 * callers -- the desktop Apple-menu item that `app.tsx` registers -- sits
 * OUTSIDE the Router, where `useNavigate` throws. So the router reaches this
 * module the same way the step-up prompt reaches `~/lib/elevationPrompt`:
 * one component inside the Router registers it.
 */
interface PreferencesAddressHandle {
  /** The category the current address asks for, or null for none. */
  category: () => string | null
  /** Writes the category onto the current address. */
  write: (category: string, replace: boolean) => void
  /** Removes the parameter from the current address, in place. */
  clear: () => void
  /** Steps back over the entry that `write` pushed. */
  back: () => void
}

/**
 * A SIGNAL rather than a plain variable, so that a read taken before the
 * Router mounts re-runs once it does. `UserMenuDialogs` and this registration
 * are siblings under the same root, and neither one may depend on which of
 * them renders first.
 */
const [address, setAddress] = createSignal<PreferencesAddressHandle | null>(null)

/**
 * Whether the open dialog owns a history entry that this app pushed.
 *
 * The address cannot carry this, and the two cases need different closes.
 * `/?prefs=account` reads the same whether `openPreferences` pushed it or the
 * user pasted it. A pushed entry must be stepped back over, or every open and
 * close leaves a copy of the current page in the history for the Back button
 * to walk through. A pasted address must NOT be stepped back over, because the
 * entry before it belongs to another site, or does not exist.
 *
 * It goes stale in one direction only, and harmlessly. The user can press Back
 * themselves, or Forward again, which opens and closes the dialog without
 * calling either function here. The next open pushes and writes the flag
 * again; a close that finds it false replaces instead, which leaves one extra
 * entry and never leaves the app.
 */
let openedByPush = false

/**
 * Registers the router with the Preferences state. Mounts once, at the app
 * root, inside the Router.
 *
 * It renders nothing. A second mount replaces the first registration, exactly
 * as a second `ElevationPromptHost` replaces the first prompter.
 */
export const PreferencesAddress: Component = () => {
  const [params, setParams] = useSearchParams<{ prefs?: string }>()
  const navigate = useNavigate()

  setAddress({
    // An empty value ('?prefs=') means the same as an absent one. The router
    // never writes it -- it drops an empty parameter -- so it can only arrive
    // from a hand-typed address.
    category: () => params[PREFERENCES_PARAM] || null,
    write: (category, replace) => setParams({ [PREFERENCES_PARAM]: category }, { replace }),
    clear: () => setParams({ [PREFERENCES_PARAM]: null }, { replace: true }),
    back: () => navigate(-1),
  })
  onCleanup(() => {
    setAddress(null)
    // The claim goes with the tree that made it. This unmounts under a running
    // app -- the app-root ErrorBoundary tears its subtree down when it catches,
    // and the desktop launcher does the same when the connection drops -- and
    // the rebuilt tree pushed nothing. A stale claim would make the next close
    // step back over an entry that this app never wrote.
    openedByPush = false
  })

  return null
}

const [showAboutDialog, setShowAboutDialog] = createSignal(false)

/**
 * The category the Preferences dialog shows, or null while it is closed.
 *
 * The name predates the address: the value is the category, and "null means
 * closed" is what makes it read as a condition at the one mount.
 */
export function showPreferencesDialog(): string | null {
  return address()?.category() ?? null
}

/**
 * Puts Preferences on `category`, from any entry point (the app menu, the
 * shortcut, the desktop menu item) -- and from the dialog's own navigation,
 * which moves an already-open dialog to another section.
 *
 * A closed dialog PUSHES, so the Back button closes it again. An open one
 * REPLACES, so browsing the sections does not fill the history.
 *
 * It does nothing while no Router exists, which is the desktop launcher before
 * it connects. The dialog cannot render there either: it mounts under the same
 * Router.
 */
export function openPreferences(category: string = DEFAULT_NAV_GROUP_ID): void {
  const here = address()
  if (!here)
    return
  const alreadyOpen = here.category() !== null
  here.write(category, alreadyOpen)
  if (!alreadyOpen)
    openedByPush = true
}

/** Close the Preferences dialog. */
export function closePreferences(): void {
  const here = address()
  if (!here)
    return
  const pushed = openedByPush
  openedByPush = false
  if (pushed)
    here.back()
  else
    here.clear()
}

export {
  setShowAboutDialog,
  showAboutDialog,
}
