import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'

/** Read Grok's explicit Bash output file for this native call and session. */
export function grokOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  const output = pickObject(native, 'rawOutput')
  const path = output?.output_file
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed') || output?.type !== 'Bash'
    || !isFilesystemPath(path)) {
    return []
  }
  const parts = path.split(/[\\/]/u)
  return parts.at(-1) === `${call.id}.log` && parts.at(-2) === 'terminal'
    && parts.at(-3) === input.resolved.agentSessionId && parts.at(-5) === 'sessions'
    ? [path]
    : []
}
