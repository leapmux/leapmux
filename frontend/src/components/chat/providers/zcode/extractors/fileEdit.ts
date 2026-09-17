import type { FileEditDiff } from '../../../ir/fileEditDiff'
import type { ReadFileResult } from '../../../ir/readFileResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { fileEditDiffFromWholeFile, fileEditHasDiff, normalizeStructuredPatchHunks } from '../../../ir/fileEditDiff'
import { parseCatNContent, readFileResultFromContent } from '../../../ir/readFileResult'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeExtractTool, zcodeToolInput } from './toolCommon'

/** These tools can supply an input diff when no explicit display exists. */
const ZCODE_DIFF_TOOLS = new Set<string>([ZCODE_TOOL.Edit, ZCODE_TOOL.Write])

/**
 * The diff a ZCode edit/write result carries.
 *
 * An explicit display supplies structured hunks for the shared diff view.
 * Edit and Write requests supply a fallback when the result omits its patch.
 * Failed calls return null so the renderer shows the error instead of an applied change.
 *
 * `ApplyPatch` cannot join {@link ZCODE_DIFF_TOOLS}, and no branch here can read its
 * envelope: ONE patch can carry several files, and this answers one diff. Its changes
 * are `ZCodeToolFacts.patchChanges`, which both halves of the card read as a list.
 */
export function extractZCodeFileDiff(row: ZCodeRow): FileEditDiff | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || update.isError)
    return null
  const display = update.result?.display
  if (display && pickString(display, 'kind') === ZCODE_DISPLAY.FileDiff) {
    const hunks = normalizeStructuredPatchHunks(display.structuredPatch)
    if (hunks && hunks.length > 0) {
      return { filePath: pickString(display, 'filePath') || zcodeFilePath(row), structuredPatch: hunks }
    }
  }
  if (!ZCODE_DIFF_TOOLS.has(row.toolName))
    return null

  // No display: fall back to what the INPUT states. A Write of a new file reports no
  // patch because there is no old side to diff against, and an Edit's input holds the
  // substitution it asked for -- which is also the only diff available on the
  // tool_use row, before the result exists.
  const input = zcodeToolInput(row)
  const filePath = pickString(input, 'file_path') || pickString(input, 'filePath')
  const source: FileEditDiff = row.toolName === ZCODE_TOOL.Write
    ? fileEditDiffFromWholeFile(filePath, pickString(input, 'content'), 'add')
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

/**
 * Build a Read result from a persisted ZCode tool row.
 *
 * ZCode returns the file already NUMBERED, in `cat -n` form (`1\talpha`), so the
 * shared cat-n parser owns the body. When the parse fails -- a binary read, or a
 * build that stops numbering -- the content is treated as plain text starting at the
 * requested offset, which is what the shared fallback renders.
 */
export function extractZCodeRead(row: ZCodeRow): ReadFileResult | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || update.isError)
    return null
  if (row.toolName !== ZCODE_TOOL.Read)
    return null

  const content = update.result?.content ?? ''
  const lines = parseCatNContent(content)
  if (lines)
    return { lines, fallbackContent: content }
  // An unnumbered body starts at the line the input asked for, which the request
  // states too -- this reads it only to number the lines it returns.
  return readFileResultFromContent({ content, startLine: pickNumber(zcodeToolInput(row), 'offset') ?? 1 })
}
