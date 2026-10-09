import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickObject, pickString } from '~/lib/jsonPick'
import { museItem, museSiblingItem } from '../protocol'

/** Expose the native path without reading its output file. */
export function museOutputFilePaths(input: RowExtractionInput): readonly string[] {
  const own = museItem(input.resolved.parentObject)
  const result = museSiblingItem(input.resolved.parentObject, input.span.result?.parentObject)
  const item = result ?? own
  const path = pickString(pickObject(item, 'outputRef'), 'path')
  return path ? [path] : []
}
