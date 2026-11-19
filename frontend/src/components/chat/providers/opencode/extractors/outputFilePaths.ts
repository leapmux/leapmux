import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { OPENCODE_NATIVE_TOOL_STATUS } from '~/generated/contracts/opencode-native-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { OPENCODE_TOOL_NAMES } from '../toolNames'
import { openCodeToolRecord, sameOpenCodeTool } from './toolCommon'

/** Read only the native result's declared filesystem pointer. */
export function openCodeOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const record = openCodeToolRecord(input.resolved.parentObject)
  if (input.span.role !== 'result' || input.spanId !== call.id || !record || record.name !== call.name
    || record.status === OPENCODE_NATIVE_TOOL_STATUS.Running) {
    return []
  }
  if (record.wireForm === 'event' && input.resolved.agentSessionId !== undefined && record.sessionId !== input.resolved.agentSessionId)
    return []
  const request = input.span.request && openCodeToolRecord(input.span.request.parentObject)
  if (request && !sameOpenCodeTool(request, record))
    return []
  const path = record.metadata.outputPath
  if (isFilesystemPath(path))
    return [path]
  const shellID = record.metadata.shellID
  if (record.name !== OPENCODE_TOOL_NAMES.Shell || record.metadata.truncated !== true || typeof shellID !== 'string' || !shellID)
    return []
  // Native shell success events omit the structured output pointer. The native
  // capture footer retains it, and progress metadata identifies its shell.
  for (const part of record.content.toReversed()) {
    if (!isObject(part) || part.type !== 'text' || typeof part.text !== 'string')
      continue
    const footer = /(?:^|\n\n)\[showing (?:lines \d+-\d+ of \d+|the last [^\n;]+); full output saved to (.+?)\](?:\n\n\[output file .+ stopped early: .+\])?$/s.exec(part.text)
    const nativePath = footer?.[1]
    if (isFilesystemPath(nativePath) && nativePath.split(/[\\/]/).at(-1) === `${shellID}.out`)
      return [nativePath]
  }
  return []
}
