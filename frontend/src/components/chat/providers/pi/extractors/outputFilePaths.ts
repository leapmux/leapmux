import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { PI_EVENT, PI_RESULT_FIELD } from '~/generated/contracts/pi-protocol'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { PI_TOOL_RESULT_FIELD } from '../protocol'

/** Read the filesystem path that the native Pi result details declare. */
export function piOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  if (input.span.role !== 'result' || native?.type !== PI_EVENT.ToolExecutionEnd
    || native[PI_RESULT_FIELD.ToolCallID] !== call.id) {
    return []
  }
  const result = pickObject(native, PI_RESULT_FIELD.Result)
  const path = pickObject(result, PI_TOOL_RESULT_FIELD.Details)?.[PI_TOOL_RESULT_FIELD.FullOutputPath]
  return isFilesystemPath(path) ? [path] : []
}
