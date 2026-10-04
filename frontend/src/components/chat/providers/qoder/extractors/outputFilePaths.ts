import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isObject, pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'

/** Read Qoder's structured persisted path from this exact native result. */
export function qoderOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  const message = pickObject(native, 'message')
  const content = message?.content
  if (input.span.role !== 'result' || native?.type !== 'user' || !Array.isArray(content)
    || !native.session_id || (input.resolved.agentSessionId && native.session_id !== input.resolved.agentSessionId)) {
    return []
  }
  const results = content.filter(isObject).filter(block => block.type === 'tool_result' && block.tool_use_id === call.id)
  if (results.length !== 1)
    return []
  const result = pickObject(native, 'tool_use_result')
  const persisted = pickObject(result, 'persistedOutput')
  const path = persisted?.path
  return isFilesystemPath(path) ? [path] : []
}
