import type { CommandResultSource } from '../../../results/commandResult'
import { pickBool, pickNumber, pickString } from '~/lib/jsonPick'

interface ClaudeBashArgs {
  toolUseResult?: Record<string, unknown> | null
  resultContent: string
  isError?: boolean
}

/**
 * Build a CommandResultSource from a Claude `Bash` tool_result. Claude's
 * structured Bash payload carries `stdout`/`stderr`/`interrupted` plus
 * persisted-output bookkeeping. There is no exit code on the wire, so the
 * status label collapses to "Interrupted" / "Error" / "Success" via
 * `commandStatusLabel`.
 *
 * When `toolUseResult` is missing, this falls back to the raw text content
 * (preserving today's behavior for subagent-style payloads).
 */
export function claudeBashFromToolResult(args: ClaudeBashArgs): CommandResultSource {
  const { toolUseResult, resultContent, isError } = args
  if (!toolUseResult) {
    return {
      output: resultContent,
      isError: isError === true,
    }
  }

  const stdout = pickString(toolUseResult, 'stdout')
  const stderr = pickString(toolUseResult, 'stderr')
  const interrupted = pickBool(toolUseResult, 'interrupted')
  const persistedOutputPath = pickString(toolUseResult, 'persistedOutputPath', undefined)
  const persistedOutputSize = pickNumber(toolUseResult, 'persistedOutputSize', undefined)
  const noOutputExpected = pickBool(toolUseResult, 'noOutputExpected') ? true : undefined

  // Concatenate stdout + stderr into a single output stream for rendering;
  // surface stderr so future styling can split them.
  const concat = stdout && stderr ? `${stdout}\n${stderr}` : (stdout || stderr || resultContent)

  return {
    output: concat,
    stderr: stderr || undefined,
    interrupted,
    isError: isError === true || interrupted,
    persistedOutputPath,
    persistedOutputSize,
    noOutputExpected,
  }
}
