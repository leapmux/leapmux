import type { FileChangeResult } from '../../../model/tools/fileChange'
import { pickObject, pickString } from '~/lib/jsonPick'
import { fileEditDiffFromOldNew } from '../../../model/fileEditDiff'

/** Read committed file bytes from the native result, independent of requested bytes. */
export function geminiCommittedFileChange(record: Record<string, unknown>): FileChangeResult | null {
  const display = pickObject(record, 'resultDisplay')
  const path = pickString(display, 'filePath')
  if (!display || !path || typeof display.originalContent !== 'string' || typeof display.newContent !== 'string')
    return null
  const change = fileEditDiffFromOldNew(path, display.originalContent, display.newContent)
  if (typeof display.isNewFile === 'boolean')
    change.operation = display.isNewFile ? 'add' : 'edit'
  return { changes: [change] }
}
