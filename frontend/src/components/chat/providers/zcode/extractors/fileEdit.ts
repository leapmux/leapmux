import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import type { ReadFileResultSource } from '../../../results/readFileResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import {
  fileEditDiffFromNewFile,
  fileEditHasDiff,
  normalizeStructuredPatchHunks,
} from '../../../results/fileEditDiff'
import { readFileSourceFromContent } from '../../../results/readFileResult'
import { parseCatNContent } from '../../../results/ReadResultView'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeExtractTool, zcodeToolInput } from './toolCommon'

/** The tools whose result carries a `file_diff` display. */
const ZCODE_DIFF_TOOLS = new Set<string>([ZCODE_TOOL.Edit, ZCODE_TOOL.Write])

/**
 * The diff a ZCode edit/write result carries.
 *
 * The app-server hands over a READY structured patch under
 * `result.display.structuredPatch`, in the same hunk shape the shared diff view
 * consumes -- so there is no diff text to parse and no old/new pair to reconstruct.
 * Returns null when the row is not an edit/write, when it failed (the error text is
 * what the body shows then), or when no display arrived.
 */
export function extractZCodeFileDiff(row: ZCodeRow): FileEditDiffSource | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || update.isError)
    return null
  if (!ZCODE_DIFF_TOOLS.has(row.toolName))
    return null

  const display = update.result?.display
  if (display && pickString(display, 'kind') === ZCODE_DISPLAY.FileDiff) {
    const hunks = normalizeStructuredPatchHunks(display.structuredPatch)
    if (hunks && hunks.length > 0) {
      return {
        filePath: pickString(display, 'filePath') || zcodeFilePath(row),
        structuredPatch: hunks,
        oldStr: '',
        newStr: '',
      }
    }
  }

  // No display: fall back to what the INPUT states. A Write of a new file reports no
  // patch because there is no old side to diff against, and an Edit's input holds the
  // substitution it asked for -- which is also the only diff available on the
  // tool_use row, before the result exists.
  const input = zcodeToolInput(row)
  const filePath = pickString(input, 'file_path') || pickString(input, 'filePath')
  const source: FileEditDiffSource = row.toolName === ZCODE_TOOL.Write
    ? fileEditDiffFromNewFile(filePath, pickString(input, 'content'))
    : {
        filePath,
        structuredPatch: null,
        oldStr: pickString(input, 'old_string'),
        newStr: pickString(input, 'new_string'),
      }
  return fileEditHasDiff(source) ? source : null
}

/**
 * The file path a row refers to, read from the display first and then from the tool
 * input.
 *
 * ZCode's own tools spell the input key `file_path`; `filePath` is accepted too so a
 * build that switches spelling does not blank every title.
 */
export function zcodeFilePath(row: ZCodeRow): string {
  const display = zcodeExtractTool(row.parsed)?.result?.display
  const fromDisplay = pickString(display, 'filePath')
  if (fromDisplay)
    return fromDisplay
  const input = zcodeToolInput(row)
  return pickString(input, 'file_path') || pickString(input, 'filePath')
}

/** A ZCode Read result plus the range its input asked for. */
export interface ZCodeReadResult {
  source: ReadFileResultSource
  offset: number | null
  limit: number | null
}

/**
 * Build a Read result from a persisted ZCode tool row.
 *
 * ZCode returns the file already NUMBERED, in `cat -n` form (`1\talpha`), so the
 * shared cat-n parser owns the body. When the parse fails -- a binary read, or a
 * build that stops numbering -- the content is treated as plain text starting at the
 * requested offset, which is what the shared fallback renders.
 */
export function extractZCodeRead(row: ZCodeRow): ZCodeReadResult | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update)
    return null
  if (row.toolName !== ZCODE_TOOL.Read)
    return null

  const input = zcodeToolInput(row)
  const offset = pickNumber(input, 'offset')
  const limit = pickNumber(input, 'limit')
  const filePath = zcodeFilePath(row)
  const content = update.result?.content ?? ''

  const lines = parseCatNContent(content)
  const source: ReadFileResultSource = lines
    ? { filePath, lines, totalLines: 0, numLines: lines.length, fallbackContent: content }
    : readFileSourceFromContent({ filePath, content, startLine: offset ?? 1 })
  return { source, offset, limit }
}
