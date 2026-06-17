import type { JSX } from 'solid-js'
import type { HeightInput } from '../chatHeightEstimator'
import type { StructuredPatchHunk } from '../diff'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { createMemo } from 'solid-js'
import { diffHeightFields, mergeDiffHeightFields } from '../chatDiffGeometry'
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

/**
 * Pre-mount height fields for a row rendering `source` as a file-edit diff, or
 * null when there is no diff to size. The shared tail of the per-provider
 * `heightMetrics` hooks that resolve a single source via `pickFileEditDiff`
 * (Claude tool_result, ACP tool_call_update): once the source is resolved the
 * row is sized identically -- the hunks' geometry plus the original-file context
 * the diff view uses for between-hunk gaps -- so the contract lives in one place
 * next to `fileEditDiffHunks` instead of being duplicated per provider.
 */
export function diffFieldsFromSource(source: FileEditDiffSource | null): Partial<HeightInput> | null {
  if (!source)
    return null
  return diffHeightFields(fileEditDiffHunks(source), source.originalFile)
}

/**
 * Pre-mount height fields for a row rendering MULTIPLE sources as N stacked
 * file-edit diff blocks (a multi-file Pi edit). Each source is sized
 * independently -- its own hunks, its own originalFile gap separators -- then
 * summed via mergeDiffHeightFields, which records diffBlockCount so the
 * estimator charges container chrome per block. Sizing the concatenated hunks
 * as one block instead would under-count chrome by (N-1) blocks and let a
 * cross-file hunk boundary spuriously trip the between-hunk separator test.
 */
export function diffFieldsFromSources(sources: FileEditDiffSource[]): Partial<HeightInput> | null {
  const slices = sources
    .map(diffFieldsFromSource)
    .filter((f): f is Partial<HeightInput> => f !== null)
  return mergeDiffHeightFields(slices)
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
