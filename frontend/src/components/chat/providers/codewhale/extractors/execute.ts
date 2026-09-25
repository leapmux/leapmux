import type { CommandResult } from '../../../model/commandResult'
import type { CommandLanguage, ExecuteRequest } from '../../../model/tools/execute'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { CODEWHALE_RESULT_METADATA } from '../protocol'

/**
 * The `git` subcommand each single-purpose Git tool runs.
 *
 * The tool NAME states it, and no argument repeats it. The `Git` facade states the
 * same word in its `action` argument instead.
 */
const GIT_SUBCOMMANDS: ReadonlyMap<string, string> = new Map<string, string>([
  [CODEWHALE_TOOL.GitStatus, 'status'],
  [CODEWHALE_TOOL.GitDiff, 'diff'],
  [CODEWHALE_TOOL.GitLog, 'log'],
  [CODEWHALE_TOOL.GitShow, 'show'],
  [CODEWHALE_TOOL.GitBlame, 'blame'],
])

/** The tools whose `code` argument is JavaScript. */
const JAVASCRIPT_TOOLS: ReadonlySet<string> = new Set<string>([CODEWHALE_TOOL.JsExecution])

/**
 * The command line one Git tool runs, from the tool name or the facade's `action`.
 *
 * The runtime composes the real argument list itself; this states the subcommand and
 * the path it was scoped to, which is what the reader needs to recognize the call.
 */
function gitCommand(toolName: string, args: Record<string, unknown>): string {
  const subcommand = GIT_SUBCOMMANDS.get(toolName) ?? pickString(args, 'action').replaceAll('_', '-')
  const path = pickString(args, 'path')
  return ['git', subcommand, path].filter(Boolean).join(' ')
}

/**
 * The command one execute call states.
 *
 * Most command tools send a `command` argument. The rest state what they run in the
 * tool name or in another argument: code for the two sandboxes, the text a terminal
 * receives, the Git subcommand, and the test runner's extra arguments. A tool that
 * states none of them heads its row with its own name and action, which is the one
 * true statement about what ran.
 */
export function codewhaleExecuteRequest(toolName: string, args: Record<string, unknown>): ExecuteRequest {
  const description = pickString(args, 'description')
  const cwd = pickString(args, 'cwd')
  const language: CommandLanguage | undefined = JAVASCRIPT_TOOLS.has(toolName) ? 'javascript' : undefined
  return {
    command: codewhaleCommandText(toolName, args),
    ...(language !== undefined ? { language } : {}),
    ...(description ? { description } : {}),
    ...(cwd ? { cwd } : {}),
  }
}

function codewhaleCommandText(toolName: string, args: Record<string, unknown>): string {
  if (toolName === CODEWHALE_TOOL.Git || GIT_SUBCOMMANDS.has(toolName))
    return gitCommand(toolName, args)
  if (toolName === CODEWHALE_TOOL.RunTests)
    return ['cargo test', pickString(args, 'args')].filter(Boolean).join(' ')
  const stated = pickString(args, 'command') || pickString(args, 'cmd') || pickString(args, 'code') || pickString(args, 'input')
  if (stated)
    return stated
  return [toolName, pickString(args, 'action')].filter(Boolean).join(' ')
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
    ...(exitCode !== null && Number.isInteger(exitCode) ? { exitCode } : {}),
    ...(durationMs !== null && durationMs >= 0 ? { durationMs } : {}),
  }
}
