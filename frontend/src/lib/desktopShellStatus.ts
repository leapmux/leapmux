import type { DesktopBehaviorRefusal } from '~/api/platformBridge'
import { createSignal } from 'solid-js'

/**
 * The desktop shell's most recent refusal of a Desktop preference, in the
 * shape the Preferences panel already places errors with.
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

const [refusal, setRefusal] = createSignal<DesktopShellRefusal | null>(null)

/** The refusal to show, or null while the shell applied everything asked. */
export const desktopShellRefusal = refusal

/**
 * Record what the shell refused, addressed to the row that owns it.
 *
 * Pass `null` after a push the shell accepted: a stale message beside a
 * control the user has since repaired is worse than none, because it reads as
 * the repair having failed too.
 */
export function reportDesktopShellRefusal(next: DesktopBehaviorRefusal | null): void {
  setRefusal(next === null ? null : { key: ROW_BY_SETTING[next.setting], message: next.message })
}

/** Return the store to its empty state. Tests only. */
export function resetDesktopShellStatusForTests(): void {
  setRefusal(null)
}
