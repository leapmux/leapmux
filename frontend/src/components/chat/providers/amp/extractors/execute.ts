import type { CommandResult } from '../../../model/commandResult'
import type { TaskResult } from '../../../model/tools/task'
import { AMP_SHELL_RESULT_FIELD } from '~/generated/contracts/amp-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/**
 * What one Amp shell tool returned.
 *
 * `shell_command`, `shell_command_status` and `shell_command_kill` answer with one JSON
 * record, which reaches the stream as a string:
 *
 *   {"output":"...","exitCode":0}                         a command that ended
 *   {"output":"...","running":true,"pid":4242}            a command Amp moved to the background
 *   {"output":"...","exitCode":0,"running":false,"pid":4242}   a status of a background command
 *
 * Amp keeps the END of a long output, and `truncation.prefixLinesOmitted` states how
 * many lines it dropped before it.
 *
 * The worker reads the exit code, the running flag and the PID too, to follow a
 * background command in the registry, so those three keys come from the contract.
 */
export interface AmpShellOutput {
  output: string
  /** The exit code, when the command ended with one. */
  exitCode?: number
  /** True while the command runs in the background. */
  running: boolean
  /** The process of a background command. */
  pid?: number
  /** Amp dropped the start of the output. */
  truncated: boolean
  /** False when the result is not Amp's record, and `output` holds the raw text. */
  structured: boolean
}

/**
 * The command of one shell call. `shell_command` states it as `command`, and the
 * legacy `Bash` tool of an old thread states it as `cmd`.
 */
export function ampShellCommand(args: Record<string, unknown>): string {
  return pickString(args, 'command') || pickString(args, 'cmd')
}

/** Read one shell result. Text that is not Amp's record is the output itself. */
export function ampShellOutput(text: string): AmpShellOutput {
  let record: unknown
  try {
    record = JSON.parse(text)
  }
  catch {
    record = undefined
  }
  if (!isObject(record) || typeof record.output !== 'string')
    return { output: text, running: false, truncated: false, structured: false }
  const exitCode = pickNumber(record, AMP_SHELL_RESULT_FIELD.ExitCode, undefined)
  const pid = pickNumber(record, AMP_SHELL_RESULT_FIELD.PID, undefined)
  const omitted = pickNumber(pickObject(record, 'truncation'), 'prefixLinesOmitted', 0)
  return {
    output: pickString(record, 'output'),
    ...(exitCode !== undefined ? { exitCode } : {}),
    running: record[AMP_SHELL_RESULT_FIELD.Running] === true,
    ...(pid !== undefined ? { pid } : {}),
    truncated: omitted > 0,
    structured: true,
  }
}

/**
 * One shell command's result.
 *
 * A command that Amp moved to the background still runs, so its result states the
 * output so far and no exit code. A result that is not Amp's record states no code
 * either: nothing says how the process ended.
 */
export function ampCommandResult(text: string): CommandResult {
  const shell = ampShellOutput(text)
  const exit = shell.exitCode !== undefined && !shell.running ? { exitCode: shell.exitCode } : {}
  return { output: shell.output, ...exit, ...(shell.truncated ? { truncated: true } : {}) }
}

/**
 * What a status or a kill of a background command reports.
 *
 * The command still runs, or it ended with a code. A kill states that Amp stopped the
 * command. Amp states the state in its record alone, not in words, so the result
 * carries no title of its own.
 */
export function ampShellTaskResult(text: string, killed: boolean): TaskResult {
  const shell = ampShellOutput(text)
  if (!shell.structured)
    return { outcome: killed ? 'stopped' : 'completed', output: text }
  if (shell.running)
    return { outcome: 'running', output: shell.output }
  if (killed)
    return { outcome: 'stopped', output: shell.output }
  if (shell.exitCode !== undefined && shell.exitCode !== 0)
    return { outcome: 'failed', output: shell.output }
  return { outcome: 'completed', output: shell.output }
}
