import { createSignal } from 'solid-js'

/**
 * The Preferences dialog's open state doubles as its deep link: the value is
 * the category id to open on (a NAV_GROUPS id, e.g. 'appearance' or
 * 'admin-email'), null means closed. Callers that only want the dialog open
 * pass 'appearance'.
 */
const [showPreferencesDialog, setShowPreferencesDialog] = createSignal<string | null>(null)

/**
 * How many times a caller asked for the Preferences dialog.
 *
 * The category alone cannot carry a repeated request. Every open path passes
 * 'appearance', and a signal notifies only on a CHANGE — so asking for
 * Preferences again while it sits on Advanced wrote the same string, notified
 * nothing, and left the dialog where it was. The counter changes on every
 * request, so the dialog's deep-link effect re-runs and returns to the
 * requested section.
 */
const [preferencesOpenSeq, setPreferencesOpenSeq] = createSignal(0)

const [showAboutDialog, setShowAboutDialog] = createSignal(false)

/**
 * Open the Preferences dialog on `category`, from any entry point (the app
 * menu, the shortcut, the user menu).
 *
 * The raw setter stays private so that no caller can open the dialog without
 * also advancing the request counter.
 */
export function openPreferences(category: string): void {
  setShowPreferencesDialog(category)
  setPreferencesOpenSeq(n => n + 1)
}

/** Close the Preferences dialog. */
export function closePreferences(): void {
  setShowPreferencesDialog(null)
}

export {
  preferencesOpenSeq,
  setShowAboutDialog,
  showAboutDialog,
  showPreferencesDialog,
}
