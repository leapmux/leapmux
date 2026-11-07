import type { CommandResult } from '../../../model/commandResult'
import type { CommandLanguage, ExecuteRequest } from '../../../model/tools/execute'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { withCommandExit } from '../../../model/commandResult'
import { CODEWHALE_RESULT_METADATA } from '../protocol'

/**
 * The command one execute call states.
 *
 * Most command tools send a `command` argument. The rest state what they run in the
 * tool name or in another argument: code for the two sandboxes, the text a terminal
 * receives, the Git subcommand. A tool that
 * states none of them heads its row with its own name and action, which is the one
 * true statement about what ran.
 */
export function codewhaleExecuteRequest(toolName: string, args: Record<string, unknown>): ExecuteRequest {
  const description = pickString(args, 'description')
  const cwd = pickString(args, 'cwd')
  const language: CommandLanguage | undefined = toolName === CODEWHALE_TOOL.JsExecution
    ? 'javascript'
    : undefined
  return {
    command: codewhaleCommandText(toolName, args),
    ...(language !== undefined
      ? { language }
      : {}),
    ...(description
      ? { description }
      : {}),
    ...(cwd
      ? { cwd }
      : {}),
  }
}

function codewhaleCommandText(toolName: string, args: Record<string, unknown>): string {
  if (toolName === CODEWHALE_TOOL.Git) {
    // The runtime composes the arguments. Display only the selected subcommand and path.
    return [
      'git',
      pickString(args, 'action').replaceAll('_', '-'),
      pickString(args, 'path'),
    ].filter(Boolean).join(' ')
  }
  const stated = pickString(args, 'command') || pickString(args, 'cmd') || pickString(args, 'code') || pickString(args, 'input')
  if (stated)
    return stated
  return [
    toolName,
    pickString(args, 'action'),
  ].filter(Boolean).join(' ')
}

/**
 * The finished command, from the result text and the item's metadata.
 *
 * `exit_code` and `duration_ms` ride in the metadata of a finished `bash` item; the
 * text is the output the command printed. A tool that states no exit code leaves it
 * absent, which the command body draws as an unknown code rather than a zero.
 */
export function codewhaleCommandResult(text: string, metadata: Record<string, unknown>): CommandResult {
  const exitCode = pickNumber(metadata, CODEWHALE_RESULT_METADATA.ExitCode)
  const durationMs = pickNumber(metadata, CODEWHALE_RESULT_METADATA.DurationMs)
  return {
    output: text,
    ...(exitCode !== null && Number.isSafeInteger(exitCode)
      ? { exitCode }
      : {}),
    ...(durationMs !== null && durationMs >= 0
      ? { durationMs }
      : {}),
  }
}

/**
 * The tool-failure prefix and the exit trailer of a command that ran and exited
 * nonzero, as the Codewhale 0.10.0 runtime words the `detail` of its failed item. The
 * item states no `exit_code`, so the trailer is the only statement of the code.
 */
const CODEWHALE_FAILURE_PREFIX = 'Failed to execute tool: '
const CODEWHALE_EXIT_TRAILER = /Command exited with code (-?\d+)$/

/**
 * The command that ran and exited nonzero, read from the words of its failed item, or
 * undefined when the words state no exit trailer: a refusal, a timeout, or a sandbox
 * denial, whose words are the reason. The runtime puts blank lines between the output
 * and the trailer, and the output loses its own final newlines with them.
 */
export function codewhaleFailedCommand(text: string, metadata: Record<string, unknown>): CommandResult | undefined {
  if (!text.startsWith(CODEWHALE_FAILURE_PREFIX))
    return undefined
  const trailer = CODEWHALE_EXIT_TRAILER.exec(text)
  const exitCode = trailer
    ? Number(trailer[1])
    : Number.NaN
  if (!trailer || trailer.index < CODEWHALE_FAILURE_PREFIX.length || !Number.isSafeInteger(exitCode))
    return undefined
  const output = text.slice(CODEWHALE_FAILURE_PREFIX.length, trailer.index).replace(/\n+$/, '')
  return withCommandExit(codewhaleCommandResult(output, metadata), { exitCode })
}
