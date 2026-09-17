import type { RunOutcome } from '../runOutcome'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../collapse'

/** A call that manages a background task by id and carries no command of its own. */
export interface TaskRequest {
  action: 'output' | 'stop' | 'list' | 'input' | 'other'
  taskId?: string
  timeoutMs?: number
  block?: boolean
}

/**
 * How the surface a task call asked about answered.
 *
 * {@link RunOutcome} minus `unknown`: a task surface always states one of the four,
 * which keeps the icon table that reads this exhaustive. It is NOT the tool-row outcome
 * of `toolOutcomeLabel`, which says how the CALL ended: a call that succeeded can
 * report a task that stopped.
 */
export type TaskOutcome = Exclude<RunOutcome, 'unknown'>

/**
 * A tool result that states an OUTCOME of its own instead of returning data.
 *
 * A background task that stopped, a message that reached a peer, a retrieval that
 * timed out: each one answers with a state and a short note, and neither the command
 * body nor the plain text body states that state. The row draws the state as its own
 * header, so the reader gets the answer before the note. `results/statusResult.tsx`
 * is the body.
 *
 * {@link TaskOutcome} states the four outcomes it can report.
 */
export interface TaskResult {
  /** The state, in the words of the surface that reports it. Absent when it states none. */
  title?: string
  outcome: TaskOutcome
  /** A command the reported operation ran. The row draws it above the note. */
  command?: string
  output: string
}

/** Whether the note holds more than the collapsed row shows. */
export function taskResultCollapsible(result: TaskResult): boolean {
  return hasMoreLinesThan(result.output, COLLAPSED_RESULT_ROWS)
}
