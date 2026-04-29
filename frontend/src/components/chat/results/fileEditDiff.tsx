import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from '../diff'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { createMemo } from 'solid-js'
import { DiffView, rawDiffToHunks } from '../diff'

/**
 * Two halves: a pre-computed `structuredPatch` (e.g. from Claude's
 * `tool_use_result`) or `oldStr`/`newStr` synthesized from a tool_use input
 * when the result message didn't carry one. `originalFile` lets the diff
 * view expand gap context past the hunks.
 */
export interface FileEditDiffSource {
  filePath: string
  structuredPatch: StructuredPatchHunk[] | null
  oldStr: string
  newStr: string
  originalFile?: string
}

/**
 * Build a FileEditDiffSource for a new-file write — the full file body
 * becomes the all-added side. Provider-neutral: used by Codex's
 * fileChange "add" rows and Pi's `write` tool.
 */
export function fileEditDiffFromNewFile(path: string, content: string): FileEditDiffSource {
  return {
    filePath: path,
    structuredPatch: null,
    oldStr: '',
    newStr: content,
  }
}

/**
 * Build a FileEditDiffSource for an edit whose unified diff has already
 * been parsed into structured hunks. Provider-neutral: used by Codex's
 * fileChange "modify" rows (hunks parsed from a unified diff) and Pi's
 * `edit` tool result (hunks parsed via parsePiNumberedDiff).
 */
export function fileEditDiffFromHunks(path: string, hunks: StructuredPatchHunk[]): FileEditDiffSource {
  return {
    filePath: path,
    structuredPatch: hunks,
    oldStr: '',
    newStr: '',
  }
}

export function fileEditHasDiff(source: FileEditDiffSource | null | undefined): source is FileEditDiffSource {
  if (!source)
    return false
  if (source.structuredPatch && source.structuredPatch.length > 0)
    return true
  // New-file write: empty old + non-empty new is an all-added diff.
  if (source.oldStr === '' && source.newStr !== '')
    return true
  return source.oldStr !== '' && source.newStr !== '' && source.oldStr !== source.newStr
}

/**
 * Result-side diff wins; otherwise fall back to the tool_use-side diff.
 * Returns null when neither has a renderable diff — callers can render the
 * return value directly without a separate `fileEditHasDiff` check.
 */
export function pickFileEditDiff(
  resultDiff: FileEditDiffSource | null | undefined,
  toolUseDiff: FileEditDiffSource | null | undefined,
): FileEditDiffSource | null {
  if (fileEditHasDiff(resultDiff))
    return resultDiff
  if (fileEditHasDiff(toolUseDiff))
    return toolUseDiff
  return null
}

/**
 * Pick the hunks to render: pre-computed `structuredPatch` when non-empty,
 * otherwise compute from `oldStr`/`newStr` via `rawDiffToHunks`.
 */
export function fileEditDiffHunks(source: FileEditDiffSource): StructuredPatchHunk[] {
  return source.structuredPatch && source.structuredPatch.length > 0
    ? source.structuredPatch
    : rawDiffToHunks(source.oldStr, source.newStr)
}

export function FileEditDiffBody(props: {
  source: FileEditDiffSource
  view: DiffViewPreference
  showLineNumbers?: boolean
}): JSX.Element {
  // Memo: DiffView reads `hunks` from several effects/memos during a single
  // render pass; without this, `rawDiffToHunks` (and the underlying
  // `diffLines`) would re-run on every read.
  const hunks = createMemo(() => fileEditDiffHunks(props.source))
  return (
    <DiffView
      hunks={hunks()}
      view={props.view}
      filePath={props.source.filePath}
      originalFile={props.source.originalFile}
      showLineNumbers={props.showLineNumbers}
    />
  )
}
