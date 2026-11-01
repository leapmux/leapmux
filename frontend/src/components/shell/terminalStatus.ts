import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import * as styles from './terminalStatus.css'

/**
 * The display state of a terminal whose worker channel the client lost. Only
 * the client can observe it, so it is no TerminalStatus value.
 */
export const TERMINAL_DISCONNECTED = 'disconnected' as const

/** A terminal status as a tab shows it: the worker's status, or the client's own disconnected state. */
export type TerminalDisplayStatus = TerminalStatus | typeof TERMINAL_DISCONNECTED

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
