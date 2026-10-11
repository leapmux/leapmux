import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { OPENCODE_TOOL_OUTPUT_FIELD } from '../protocol'

/** Read a declared native output path. Keep the inline output unchanged. */
export function openCodeOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed')) {
    return []
  }
  const output = pickObject(native, 'rawOutput')
  const metadata = pickObject(output, OPENCODE_TOOL_OUTPUT_FIELD.Metadata)
  const path = metadata?.[OPENCODE_TOOL_OUTPUT_FIELD.OutputPath]
  return isFilesystemPath(path) && /[\\/]tool-output[\\/]tool_[A-Za-z0-9]+$/u.test(path) ? [path] : []
}
