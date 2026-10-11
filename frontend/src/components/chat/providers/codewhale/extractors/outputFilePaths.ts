import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { CODEWHALE_ENVELOPE_FIELD, CODEWHALE_EVENT, CODEWHALE_ITEM_FIELD, CODEWHALE_ITEM_METADATA } from '~/generated/contracts/codewhale-protocol'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'

/** Read the explicit spill path from the exact native item metadata. */
export function codewhaleOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  const item = pickObject(pickObject(native, CODEWHALE_ENVELOPE_FIELD.Payload), CODEWHALE_ITEM_FIELD.Item)
  const metadata = pickObject(item, CODEWHALE_ITEM_FIELD.Metadata)
  if (input.span.role !== 'result' || native?.[CODEWHALE_ENVELOPE_FIELD.Event] !== CODEWHALE_EVENT.ItemCompleted
    || metadata?.[CODEWHALE_ITEM_METADATA.ToolUseID] !== call.id
    || (input.resolved.agentSessionId && native[CODEWHALE_ENVELOPE_FIELD.ThreadID] !== input.resolved.agentSessionId)) {
    return []
  }
  const path = metadata[CODEWHALE_ITEM_METADATA.SpilloverPath]
  if (isFilesystemPath(path))
    return [path]
  const detail = item?.[CODEWHALE_ITEM_FIELD.Detail]
  if (typeof detail !== 'string')
    return []
  const pointers = [...detail.matchAll(/\[Showing lines \d+-\d+ of \d+ \([^\r\n]+ limit\)\. Full output: ([^\]\r\n]+)\]$/gmu)]
  const footer = pointers.length === 1 ? pointers[0]?.[1] : undefined
  return isFilesystemPath(footer) ? [footer] : []
}
