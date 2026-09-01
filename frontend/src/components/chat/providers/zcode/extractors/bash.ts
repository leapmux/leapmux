import type { CommandResultSource } from '../../../results/commandResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { zcodeErrorText, zcodeExtractTool, zcodeToolInput } from './toolCommon'

/** The `perf.detail.kind` that marks a command's telemetry block. */
const ZCODE_PERF_COMMAND = 'command'

export interface ZCodeBashCommand {
  command: string
  description: string
  /** Process output, verbatim from `result.content`. */
  output: string
  /**
   * Exit code from `result.perf.detail.command.exitCode`. Null when the app-server
   * reported no command telemetry -- a refused call, or a build that omits it.
   *
   * ZCode reports a ZERO exit code explicitly, unlike providers that only surface
   * failures, so null genuinely means "unknown" and never "succeeded".
   */
  exitCode: number | null
  timedOut: boolean
  truncated: boolean
  isError: boolean
  /** Wall-clock time of the call, from the update's own `duration`. */
  durationMs: number | null
}

/**
 * Build a bash result from a persisted ZCode tool row.
 *
 * The command itself lives on the SCHEDULED row's input, never on the result, so
 * `toolUseParsed` supplies it for a result row. Returns null for a row that is not
 * a Bash tool call.
 */
export function extractZCodeBash(row: ZCodeRow): ZCodeBashCommand | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update)
    return null
  if (row.toolName !== ZCODE_TOOL.Bash)
    return null

  const input = zcodeToolInput(row)
  const command = pickString(input, 'command')
  const detail = update.result?.perfDetail
  // The detail block is per-kind; reading `command` off a patch detail would pick up
  // an unrelated shape, so the kind is checked before the block is trusted.
  const commandPerf = pickString(detail, 'kind') === ZCODE_PERF_COMMAND
    ? pickObject(detail, 'command')
    : null

  return {
    command,
    description: pickString(input, 'description'),
    output: update.isError ? zcodeErrorText(update) : (update.result?.content ?? ''),
    exitCode: pickNumber(commandPerf, 'exitCode'),
    timedOut: commandPerf?.timedOut === true,
    truncated: update.result?.truncated === true,
    isError: update.isError,
    durationMs: update.durationMs,
  }
}

/**
 * Adapt a ZCode bash result to the shared command-result shape.
 *
 * A non-zero exit is an error even when the app-server did not mark the call one:
 * it reports a failed command as a SUCCESSFUL tool call whose content says
 * "Exit code 3", so the exit code is the only signal that the command failed.
 */
export function zcodeBashToCommandSource(bash: ZCodeBashCommand): CommandResultSource {
  return {
    output: bash.output,
    exitCode: bash.exitCode ?? undefined,
    durationMs: bash.durationMs,
    interrupted: bash.timedOut,
    isError: bash.isError || bash.timedOut || (bash.exitCode != null && bash.exitCode !== 0),
  }
}
