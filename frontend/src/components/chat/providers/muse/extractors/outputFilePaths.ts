import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { pickObject, pickString } from '~/lib/jsonPick'
import { museItem } from '../protocol'

/** Expose the native path of the row's own item without reading its output file. */
export function museOutputFilePaths(input: RowExtractionInput): readonly string[] {
  const path = pickString(pickObject(museItem(input.resolved.parentObject), 'outputRef'), 'path')
  return path
    ? [path]
    : []
}
