import type { CommandResult } from '../../../model/commandResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { splitExitCodeMarker } from '../../../model/exitCodeMarker'
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
  /**
   * True when the app-server sent the command telemetry block for this call.
   *
   * Separate from `exitCode`, which is null both for an ABSENT block and for a block
   * that stated no code. Only the second contradicts a leading `Exit code N` line: the
   * formatter that sends the block is the one that writes the line, so a block with no
   * code in it says that the line came from somewhere else.
   */
  hasCommandTelemetry: boolean
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
    hasCommandTelemetry: commandPerf !== null && Object.keys(commandPerf).length > 0,
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
 *
 * That same line is also the content's first line, so it is consumed here and the
 * code reaches the reader once. When the app-server sent no telemetry the line is
 * the only statement of the code, and it supplies it.
 */
export function zcodeBashToCommandResult(bash: ZCodeBashCommand): CommandResult {
  // ZCode states the exit code TWICE: as this structured field, and as the first
  // line of the content. The line is consumed so the reader sees the code once, in
  // the status label.
  const marked = splitExitCodeMarker(bash.output)
  const markerUsable = marked.exitCode !== undefined && exitMarkerBelongsToCall(bash, marked.exitCode)
  const exitCode = bash.exitCode ?? (markerUsable ? marked.exitCode : undefined)
  return {
    output: markerUsable ? marked.output : bash.output,
    ...(exitCode !== undefined ? { exitCode } : {}),
    durationMs: bash.durationMs,
  }
}

/**
 * Does the leading `Exit code N` line state THIS call's outcome?
 *
 * The structured field answers directly when the app-server sent one: the line states
 * the same code, or it does not. A DIFFERENCE keeps the line, because the difference is
 * worth showing, and the label still states the structured field -- the app-server
 * computed that one rather than formatted it.
 *
 * With no structured field, nothing compares. The marker then stands unless another
 * field of the call contradicts it, because `splitExitCodeMarker` anchors to the start
 * of the TEXT and not to a frame the app-server drew -- so a command whose OWN output
 * begins with those words reaches here too. Two fields contradict the line:
 *
 *   - A telemetry block that arrived with NO exit code in it. The formatter that sends
 *     the block is the one that writes the line.
 *   - A call the app-server marked an error, under a line that claims exit 0.
 *
 * The reverse of the second case is not a contradiction, and neither is an absent
 * telemetry block. ZCode reports a failed command as a SUCCESSFUL tool call whose
 * content states the code, and a build that sends no telemetry leaves the line as the
 * only statement of it.
 */
function exitMarkerBelongsToCall(bash: ZCodeBashCommand, markerExitCode: number): boolean {
  if (bash.exitCode !== null)
    return markerExitCode === bash.exitCode
  if (bash.hasCommandTelemetry)
    return false
  return !(bash.isError && markerExitCode === 0)
}
