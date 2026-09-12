import type { CommandResultSource } from '../../../results/commandResult'
import { pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { commandIsError } from '../../../results/commandResult'
import { collectAcpToolText, pickAcpRawOutputMetadata } from '../content'

/**
 * Build a CommandResultSource from an ACP `tool_call_update` of kind
 * `execute`. Reads `rawInput.command`, `rawOutput.metadata.exit`, and the
 * collected text output. The status label resolves via `commandStatusLabel`.
 */
export function acpExecuteFromToolCall(toolUse: Record<string, unknown> | null | undefined): CommandResultSource | null {
  if (!toolUse)
    return null
  const exitCode = pickNumber(pickAcpRawOutputMetadata(toolUse), 'exit')
  const rawOutput = pickObject(toolUse, 'rawOutput')
  return {
    output: collectAcpToolText(toolUse, { rawObjects: false }),
    exitCode,
    truncated: pickBoolean(rawOutput, 'truncated') ?? false,
    isError: commandIsError(pickString(toolUse, 'status'), exitCode),
  }
}
