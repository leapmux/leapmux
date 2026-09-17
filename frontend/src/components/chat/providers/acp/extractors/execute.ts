import type { CommandResult } from '../../../ir/commandResult'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { pickBoolean, pickNumber, pickObject } from '~/lib/jsonPick'
import { collectAcpToolText, pickAcpRawOutputMetadata } from '../content'

/**
 * Build a CommandResult from an ACP `tool_call_update` of kind
 * `execute`. Reads `rawInput.command`, `rawOutput.metadata.exit`, and the
 * collected text output. The status label resolves via `commandStatusLabel`.
 */
export function acpExecuteFromToolCall(toolUse: Record<string, unknown> | null | undefined): CommandResult | null {
  if (!toolUse)
    return null
  const exitCode = pickNumber(pickAcpRawOutputMetadata(toolUse), 'exit')
  const rawOutput = pickObject(toolUse, ACP_SUPPLEMENT.RawOutput)
  return {
    output: collectAcpToolText(toolUse, { rawObjects: false }),
    exitCode,
    truncated: pickBoolean(rawOutput, 'truncated') ?? false,
  }
}
