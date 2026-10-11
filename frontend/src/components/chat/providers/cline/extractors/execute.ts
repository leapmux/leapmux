import type { CommandResult } from '../../../model/commandResult'
import type { ExecuteRequest } from '../../../model/tools/execute'
import type { ClineOperation } from './toolCommon'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { clineOperations } from './toolCommon'

/**
 * Cline's `run_commands` tool.
 *
 *   {commands: ["git status", {command: "go", args: ["test", "./..."]}]}
 *     ->  [{query, result, error?, success}, ...]
 *
 * One call runs several commands in order, and its result holds one record for each.
 * A record states the command as `query` and its whole output as `result`, and a
 * command that failed states why in `error`. A failed command does not fail the call:
 * Cline sets no error on the call, so the call completes.
 *
 * Cline states no exit code as a field. A command that exited with a code other than
 * zero states the code in words, twice: on the first line of `result`, and as the
 * whole `error` (`CommandExitError` in `sdk/packages/core/src/extensions/tools/
 * executors/bash.ts`, and `executeShellCommands` in `definitions.ts`):
 *
 *   {query, result: "[Command exited with code 3]\n<output>", error: "Command exited with code 3", success: false}
 *
 * `<output>` is the command's stdout, then `\n[stderr]\n` and its stderr when it wrote
 * to stderr. A command that did not start, or that ran out of time, states no code:
 *
 *   {query, result: "", error: "Command failed: <reason>", success: false}
 */

/** The error of a command that exited with a code other than zero, with that code. */
const CLINE_EXIT_ERROR = /^Command exited with code (-?\d+)$/

/** The text of one command: a line of shell, or a program and its arguments. */
function commandText(command: unknown): string {
  if (typeof command === 'string')
    return command
  if (!isObject(command))
    return ''
  return [pickString(command, 'command'), ...stringArray(command.args)].filter(Boolean).join(' ')
}

/** The commands one call states, a line each. */
export function clineCommandRequest(args: Record<string, unknown>): ExecuteRequest {
  const commands = Array.isArray(args.commands)
    ? args.commands.map(commandText).filter(Boolean)
    : [pickString(args, 'command')].filter(Boolean)
  return { command: commands.join('\n'), language: 'bash' }
}

/**
 * The exit code that a failed record states in its error, or undefined.
 *
 * Only a failed record has a code to read: a command that succeeded can print the same
 * words. Cline states a code other than zero only, so a zero on a failed record states
 * no code. A number past the safe integers states no code either, because no platform
 * reports one.
 */
function clineExitCode(operation: ClineOperation): number | undefined {
  if (operation.success)
    return undefined
  const digits = CLINE_EXIT_ERROR.exec(operation.error.trim())?.[1]
  if (digits === undefined)
    return undefined
  const code = Number(digits)
  return Number.isSafeInteger(code) && code !== 0 ? code : undefined
}

/**
 * The result without the line that opens it and states `code`, and without the line
 * break that Cline puts after that line.
 *
 * A result that does not open with that line is the command's output as it stands, so
 * it stays whole. A line that states another code also stays, because the reader must
 * see the difference.
 */
function withoutExitLine(result: string, code: number): string {
  const line = `[Command exited with code ${code}]`
  if (result === line)
    return ''
  return result.startsWith(`${line}\n`) ? result.slice(line.length + 1) : result
}

/**
 * One result for each command a call ran.
 *
 * The exit code of a failed command goes to the result, where the row's header states
 * it. The body then drops the words that state the code again, so it opens with the
 * command's own output. A collapsed row then shows why the command failed.
 *
 * A failed command with no code -- one that did not start, or that ran out of time --
 * states its failure through `failed`, so the header states it too. Its error has no
 * other place, so it follows the output on a line of its own.
 */
export function clineCommandResults(output: unknown): CommandResult[] {
  return clineOperations(output).map((operation) => {
    const label = operation.query ? { label: operation.query } : {}
    const exitCode = clineExitCode(operation)
    if (exitCode !== undefined)
      return { output: withoutExitLine(operation.result, exitCode), exitCode, ...label }
    const separator = operation.result === '' || operation.result.endsWith('\n') ? '' : '\n'
    const text = operation.error ? `${operation.result}${separator}${operation.error}` : operation.result
    return operation.success ? { output: text, ...label } : { output: text, failed: true, ...label }
  })
}
