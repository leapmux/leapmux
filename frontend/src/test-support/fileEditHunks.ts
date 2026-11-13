import type { StructuredPatchHunk } from '~/components/chat/diff/diffTypes'
import type { FileEditDiff } from '~/components/chat/model/fileEditDiff'
import { computeDiff } from '~/components/chat/diff/diffSearch'
import { fileEditDiffInput } from '~/components/chat/model/fileEditDiff'

/**
 * The hunks that one file edit draws, computed on the calling thread exactly as
 * the diff worker computes them.
 */
export function fileEditDrawnHunks(source: FileEditDiff): StructuredPatchHunk[] {
  return computeDiff(fileEditDiffInput(source)).hunks
}
