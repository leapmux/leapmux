import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { MIMO_OUTPUT_FILE_FIELD, MIMO_PART_FIELD } from '../protocol'

/** Read the path that the native HTTP tool part declares. */
export function mimoOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const frame = input.resolved.parentObject
  const properties = pickObject(frame, MIMO_PART_FIELD.Properties)
  const part = pickObject(properties, MIMO_PART_FIELD.Part)
  const state = pickObject(part, MIMO_PART_FIELD.State)
  if (input.span.role !== 'result' || frame?.type !== MIMO_EVENT.MessagePartUpdated
    || part?.[MIMO_PART_FIELD.Type] !== MIMO_PART_TYPE.Tool || part[MIMO_PART_FIELD.CallID] !== call.id
    || (state?.status !== MIMO_TOOL_STATUS.Completed && state?.status !== MIMO_TOOL_STATUS.Error)
    || (properties?.[MIMO_PART_FIELD.SessionID] !== undefined && properties[MIMO_PART_FIELD.SessionID] !== part[MIMO_PART_FIELD.SessionID])
    || (input.resolved.agentSessionId && input.resolved.agentSessionId !== part[MIMO_PART_FIELD.SessionID])) {
    return []
  }
  const path = pickObject(state, MIMO_OUTPUT_FILE_FIELD.Metadata)?.[MIMO_OUTPUT_FILE_FIELD.OutputPath]
  return isFilesystemPath(path) && /[\\/]tool-output[\\/]tool_[A-Za-z0-9]+$/u.test(path) ? [path] : []
}
