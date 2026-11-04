import type { TerminalDisplayStatus } from '~/stores/tab.types'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TERMINAL_DISCONNECTED } from '~/stores/tab.types'
import * as styles from './terminalStatus.css'

// Static classList objects — avoids per-render allocation from tab rendering.
const RUNNING_OR_UNSET: Record<string, boolean> = {}
const DISCONNECTED: Record<string, boolean> = { [styles.disconnected]: true }
const EXITED: Record<string, boolean> = { [styles.exited]: true }

/** classList bindings for a span whose style depends on terminal status. */
export function terminalStatusClassList(status: TerminalDisplayStatus | undefined): Record<string, boolean> {
  switch (status) {
    case TERMINAL_DISCONNECTED: return DISCONNECTED
    case TerminalStatus.EXITED: return EXITED
    default: return RUNNING_OR_UNSET
  }
}
