import { TerminalStatus } from '~/stores/tab.store'
import * as styles from './terminalStatus.css'

// Static classList objects — avoids per-render allocation from tab rendering.
const RUNNING_OR_UNSET: Record<string, boolean> = {}
const DISCONNECTED: Record<string, boolean> = { [styles.disconnected]: true }
const EXITED: Record<string, boolean> = { [styles.exited]: true }

/** classList bindings for a span whose style depends on terminal status. */
export function terminalStatusClassList(status: TerminalStatus | undefined): Record<string, boolean> {
  switch (status) {
    case TerminalStatus.DISCONNECTED: return DISCONNECTED
    case TerminalStatus.EXITED: return EXITED
    default: return RUNNING_OR_UNSET
  }
}
