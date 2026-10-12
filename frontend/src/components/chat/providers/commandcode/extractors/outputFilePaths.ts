import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { COMMAND_CODE_EVENT, COMMAND_CODE_TOOL } from '~/generated/contracts/commandcode-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { commandCodeEvent } from '../protocol'

const OUTPUT_NOTICE = /\n\[\.\.\. output truncated: ([\d,]+) chars total, showing first ([\d,]+) and last part \.\.\.\]\n\[full output saved to: ([^\r\n]+) — read it with read_file \(offset\/limit\) or grep\]\n/gu

/** Read the explicit native shell pointer without replacing the preview. */
export function commandCodeOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const event = commandCodeEvent(input.resolved.parentObject)
  if (input.span.role !== 'result' || event?.type !== COMMAND_CODE_EVENT.ToolCompleted
    || event.toolCallId !== call.id || event.toolName !== COMMAND_CODE_TOOL.ShellCommand || !Array.isArray(event.result)) {
    return []
  }
  const matches = event.result.filter(isObject).filter(block => block.type === 'text' && typeof block.text === 'string').flatMap(block => typeof block.text === 'string' ? [...block.text.matchAll(OUTPUT_NOTICE)] : [])
  const match = matches.length === 1 ? matches[0] : undefined
  if (!match)
    return []
  const count = Number(match[1]?.replaceAll(',', ''))
  const head = Number(match[2]?.replaceAll(',', ''))
  const path = match[3]
  return Number.isSafeInteger(count) && Number.isSafeInteger(head) && count > 0 && head >= 0 && head <= count
    && isFilesystemPath(path)
    ? [path]
    : []
}
