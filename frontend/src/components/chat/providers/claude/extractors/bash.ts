import type { CommandResultSource } from '../../../results/commandResult'
import { pickBool, pickString } from '~/lib/jsonPick'
import { splitExitCodeMarker } from '../../../results/exitCodeMarker'

interface ClaudeBashArgs {
  toolUseResult?: Record<string, unknown> | null
  resultContent: string
  isError?: boolean
}

/**
 * Build a CommandResultSource from a Claude `Bash` tool_result. Claude's
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
export function claudeBashFromToolResult(args: ClaudeBashArgs): CommandResultSource {
  const { toolUseResult, resultContent, isError } = args
  if (!toolUseResult) {
    // Zero is a real exit code, not an absent one. `commandStatusLabel` already
    // refuses to call it a failure, so carrying it changes no label.
    const { output, exitCode } = splitExitCodeMarker(resultContent)
    return {
      output,
      ...(exitCode === undefined ? {} : { exitCode }),
      isError: isError === true,
    }
  }

  const stdout = pickString(toolUseResult, 'stdout')
  const stderr = pickString(toolUseResult, 'stderr')
  const interrupted = pickBool(toolUseResult, 'interrupted')

  // Concatenate stdout + stderr into a single output stream for rendering;
  // surface stderr so future styling can split them.
  const concat = stdout && stderr ? `${stdout}\n${stderr}` : (stdout || stderr || resultContent)

  return {
    output: concat,
    stderr: stderr || undefined,
    interrupted,
    isError: isError === true || interrupted,
  }
}
