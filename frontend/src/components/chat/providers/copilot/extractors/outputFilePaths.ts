import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { copilotEventData } from '../protocol'

/** Read each explicit filesystem path from the exact native shell completion. */
export function copilotOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = input.resolved.parentObject
  const data = copilotEventData(native, COPILOT_EVENT.ToolCompleted)
  const result = pickObject(data, 'result')
  const params = pickObject(native, 'params')
  if (input.span.role !== 'result' || data?.toolCallId !== call.id
    || (input.resolved.agentSessionId && params?.sessionId !== input.resolved.agentSessionId)
    || !Array.isArray(result?.contents)) {
    return []
  }
  return [...new Set(result.contents.filter(isObject)
    .filter(block => block.type === 'shell_exit')
    .map(block => block.outputFilePath)
    .filter((path): path is string => isFilesystemPath(path)))]
}
