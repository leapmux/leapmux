import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { KIRO_OUTPUT_TRANSFORMATION, KIRO_OUTPUT_TRANSFORMATION_KEY, KIRO_OUTPUT_TRANSFORMATION_KIND, kiroMeta } from '../protocol'

/** Read an offloaded path from the native Kiro metadata. */
export function kiroOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed')) {
    return []
  }
  const transformation = pickObject(kiroMeta(native), KIRO_OUTPUT_TRANSFORMATION_KEY)
  const path = transformation?.[KIRO_OUTPUT_TRANSFORMATION.Path]
  return transformation?.[KIRO_OUTPUT_TRANSFORMATION.Kind] === KIRO_OUTPUT_TRANSFORMATION_KIND.Offloaded
    && isFilesystemPath(path) && /[\\/]tool-outputs[\\/][\w-]+-[a-f0-9]{8}\.txt$/iu.test(path)
    ? [path]
    : []
}
