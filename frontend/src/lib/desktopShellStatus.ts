import type { DesktopBehaviorRefusal } from '~/api/platformBridge'
import { createSignal } from 'solid-js'

/**
 * What the desktop shell refused of the last Desktop push, in the shape the
 * Preferences panel already places errors with.
 *
 * A MODULE-LEVEL signal rather than a context value, for the same reason
 * `~/lib/themeStore` is one: the writer (`useDesktopWindowBehavior`, mounted
 * once near the root) and the reader (the Preferences dialog, mounted and
 * unmounted at will) have no useful provider between them, and the fact
 * belongs to the machine rather than to any component's lifetime.
 *
 * The preference itself is always stored. This is what the operating system
 * did about it, which is a different question -- turning the tray on where no
 * status-icon library exists saves the choice and shows no icon, and only this
 * message tells the user which happened.
 */
export interface DesktopShellRefusal {
  /** The Preferences row id the message belongs beside. */
  key: string
  message: string
}

/** The Preferences row that owns each refusable choice. */
const ROW_BY_SETTING: Record<DesktopBehaviorRefusal['setting'], string> = {
  trayEnabled: 'desktop.trayEnabled',
  startOnLogin: 'desktop.startOnLogin',
}

const [refusals, setRefusals] = createSignal<readonly DesktopShellRefusal[]>([])

/**
 * Every refusal to show, empty while the shell applied everything asked.
 *
 * A LIST, because the two refusable choices fail independently and each
 * message belongs on its own row: a Linux desktop with no status-icon library
 * can also be one whose operating system declines a login item, and reporting
 * one of those would leave the other toggle reading "on" with no explanation.
 */
export const desktopShellRefusals = refusals

/**
 * Record what the shell refused, each addressed to the row that owns it.
 *
 * Pass an empty list after a push the shell accepted: a stale message beside a
 * control the user has since repaired is worse than none, because it reads as
 * the repair having failed too.
 */
export function reportDesktopShellRefusals(next: readonly DesktopBehaviorRefusal[]): void {
  setRefusals(next.map(r => ({ key: ROW_BY_SETTING[r.setting], message: r.message })))
}

/** Return the store to its empty state. Tests only. */
export function resetDesktopShellStatusForTests(): void {
  setRefusals([])
}
