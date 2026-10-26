import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isFilesystemPath } from '~/lib/paths'
import { flattenAcpContent } from '../../acp/content'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { GOOSE_OUTPUT_REFERENCE } from '../protocol'
import { gooseToolIdentity } from '../toolIdentity'
import { GOOSE_DEVELOPER_EXTENSION, GOOSE_DEVELOPER_TOOL } from '../toolKinds'

/** Preserve the native preview and expose the files stated by this shell result. */
export function gooseOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed')) {
    return []
  }
  const { name, extension } = gooseToolIdentity(native)
  if (extension !== GOOSE_DEVELOPER_EXTENSION || name !== GOOSE_DEVELOPER_TOOL.Shell)
    return []
  const paths = new Set<string>()
  for (const block of flattenAcpContent(native.content)) {
    if (block.type !== 'text' || typeof block.text !== 'string')
      continue
    for (const line of block.text.split('\n')) {
      const path = GOOSE_OUTPUT_REFERENCE.Notice.exec(line)?.[1]
      if (path && isFilesystemPath(path) && !path.includes('\r')
        && GOOSE_OUTPUT_REFERENCE.Filename.test(path.split(/[\\/]/u).at(-1) ?? '')) {
        paths.add(path)
      }
    }
  }
  return [...paths]
}
