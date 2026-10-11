import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { openCodeOutputFilePaths } from '../../opencode/extractors/outputFilePaths'

/** Kilo uses the OpenCode family's native output metadata. */
export function kiloOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  return openCodeOutputFilePaths(input, call)
}
