import type { StructuredPatchHunk } from '../../../diff'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'

/**
 * Build a FileEditDiffSource for a Codex `fileChange` entry that adds a new
 *  file. The full file body becomes the all-added side.
 */
export function codexFileEditFromAdd(path: string, content: string): FileEditDiffSource {
  return {
    filePath: path,
    structuredPatch: null,
    oldStr: '',
    newStr: content,
  }
}

/**
 * Build a FileEditDiffSource for a Codex `fileChange` entry whose unified
 *  diff has already been parsed into structured hunks.
 */
export function codexFileEditFromHunks(path: string, hunks: StructuredPatchHunk[]): FileEditDiffSource {
  return {
    filePath: path,
    structuredPatch: hunks,
    oldStr: '',
    newStr: '',
  }
}
