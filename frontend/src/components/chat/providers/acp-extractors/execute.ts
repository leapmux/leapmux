import type { CommandResultSource } from '../../results/commandResult'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { collectAcpToolText } from '../shared/acpRendering'

/**
 * Build a CommandResultSource from an ACP `tool_call_update` of kind
 * `execute`. Reads `rawInput.command`, `rawOutput.metadata.exit`, and the
 * collected text output. The status label resolves via `commandStatusLabel`.
 */
export function acpExecuteFromToolCall(toolUse: Record<string, unknown> | null | undefined): CommandResultSource | null {
  if (!toolUse)
    return null
  const error = pickString(toolUse, 'status') === 'failed'
  const rawOutput = isObject(toolUse.rawOutput) ? toolUse.rawOutput as Record<string, unknown> : null
  const metadata = rawOutput && isObject(rawOutput.metadata) ? rawOutput.metadata as Record<string, unknown> : null
  const exitCode = pickNumber(metadata, 'exit')

  const isError = error || (exitCode !== null && exitCode !== 0)

  return {
    output: collectAcpToolText(toolUse),
    exitCode,
    isError,
  }
}
