import type { CommandResult } from '../../../model/commandResult'
import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ExecuteRequest } from '../../../model/tools/execute'
import type { ClaudeToolRow } from './toolCommon'
import { pickString } from '~/lib/jsonPick'
import { splitExitCodeMarker } from '../../../model/exitCodeMarker'

interface ClaudeBashArgs {
  toolUseResult?: Record<string, unknown> | null
  resultContent: string
}

/**
 * Build a CommandResult from a Claude `Bash` tool_result. Claude's
 * structured Bash payload carries `stdout`/`stderr`/`interrupted` and no exit
 * code, so the status label collapses to "Interrupted" / "Error" / "Success"
 * via `commandStatusLabel`.
 *
 * When `toolUseResult` is missing, this falls back to the raw text content
 * (preserving today's behavior for subagent-style payloads). A FAILED command
 * takes that path, and its text states the exit code in the first line. Reading
 * it there is the only way this row can say "Error (exit 1)" like OpenCode, Pi
 * and ZCode do for the same failure.
 */
export function claudeBashFromToolResult(args: ClaudeBashArgs): CommandResult {
  const { toolUseResult, resultContent } = args
  if (!toolUseResult) {
    // Zero is a real exit code, not an absent one. `commandStatusLabel` already
    // refuses to call it a failure, so carrying it changes no label.
    const { output, exitCode } = splitExitCodeMarker(resultContent)
    return {
      output,
      ...(exitCode === undefined ? {} : { exitCode }),
    }
  }

  const stdout = pickString(toolUseResult, 'stdout')
  const stderr = pickString(toolUseResult, 'stderr')

  // Concatenate stdout + stderr into a single output stream for rendering;
  // surface stderr so future styling can split them.
  const concat = stdout && stderr ? `${stdout}\n${stderr}` : (stdout || stderr || resultContent)

  return {
    output: concat,
  }
}

/**
 * The execute pair of a `Bash` or `PowerShell` call: the command it ran and the
 * stream it printed. A call that has not returned states the command alone.
 */
export function claudeExecuteSpec(request: ExecuteRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'execute'> {
  if (!result)
    return { kind: 'execute', request }
  return {
    kind: 'execute',
    request,
    result: {
      // The record is absent, not null: the arg type takes null as a stated
      // "no structured payload", which a row that carries no record does not state.
      commands: [claudeBashFromToolResult({
        ...(result.toolUseResult !== undefined ? { toolUseResult: result.toolUseResult } : {}),
        resultContent: result.resultContent,
      })],
      unresolvedTerminals: [],
    },
  }
}
