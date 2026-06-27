import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from '../diff'
import type { RenderContext } from '../messageRenderers'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { createMemo } from 'solid-js'
import { DiffView, rawDiffToHunks } from '../diff'
import { cachedRenderValueForStrings } from '../messageRenderCache'

const NO_NEWLINE_MARKER = '\\ No newline at end of file'

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

function isNonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function normalizeStructuredPatchHunk(value: unknown): StructuredPatchHunk | null {
  if (typeof value !== 'object' || value === null)
    return null
  const hunk = value as Partial<StructuredPatchHunk>
  if (!isNonNegativeSafeInteger(hunk.oldStart)
    || !isNonNegativeSafeInteger(hunk.oldLines)
    || !isNonNegativeSafeInteger(hunk.newStart)
    || !isNonNegativeSafeInteger(hunk.newLines)
    || !Array.isArray(hunk.lines)
    || hunk.lines.length === 0) {
    return null
  }

  let filteredLines: string[] | undefined
  let oldLineCount = 0
  let newLineCount = 0
  for (let i = 0; i < hunk.lines.length; i++) {
    const line = hunk.lines[i]
    if (typeof line !== 'string')
      return null
    if (line.startsWith(NO_NEWLINE_MARKER)) {
      filteredLines ??= hunk.lines.slice(0, i)
      continue
    }
    if (line.length === 0 || (line[0] !== ' ' && line[0] !== '+' && line[0] !== '-'))
      return null
    if (line[0] !== '+')
      oldLineCount++
    if (line[0] !== '-')
      newLineCount++
    filteredLines?.push(line)
  }
  if (oldLineCount !== hunk.oldLines || newLineCount !== hunk.newLines)
    return null

  return filteredLines
    ? {
        oldStart: hunk.oldStart,
        oldLines: hunk.oldLines,
        newStart: hunk.newStart,
        newLines: hunk.newLines,
        lines: filteredLines,
      }
    : (hunk as StructuredPatchHunk)
}

export function normalizeStructuredPatchHunks(hunks: unknown): StructuredPatchHunk[] | null {
  if (!Array.isArray(hunks))
    return null
  let normalizedHunks: StructuredPatchHunk[] | undefined
  for (let i = 0; i < hunks.length; i++) {
    const normalized = normalizeStructuredPatchHunk(hunks[i])
    if (!normalized)
      return null
    if (normalizedHunks) {
      normalizedHunks.push(normalized)
    }
    else if (normalized !== hunks[i]) {
      normalizedHunks = (hunks.slice(0, i) as StructuredPatchHunk[]).concat(normalized)
    }
  }
  return normalizedHunks ?? (hunks as StructuredPatchHunk[])
}

function nonEmptyStructuredPatch(source: FileEditDiffSource): StructuredPatchHunk[] | null {
  const hunks = normalizeStructuredPatchHunks(source.structuredPatch)
  return hunks && hunks.length > 0 ? hunks : null
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
    structuredPatch: normalizeStructuredPatchHunks(hunks),
    oldStr: '',
    newStr: '',
  }
}

export function fileEditHasDiff(source: FileEditDiffSource | null | undefined): source is FileEditDiffSource {
  if (!source)
    return false
  if (nonEmptyStructuredPatch(source))
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
  return nonEmptyStructuredPatch(source) ?? rawDiffToHunks(source.oldStr, source.newStr)
}

export function FileEditDiffBody(props: {
  source: FileEditDiffSource
  view: DiffViewPreference
  showLineNumbers?: boolean
  context?: RenderContext
}): JSX.Element {
  // Memo: DiffView reads `hunks` from several effects/memos during a single
  // render pass; without this, `rawDiffToHunks` (and the underlying
  // `diffLines`) would re-run on every read.
  const hunks = createMemo(() => {
    const source = props.source
    const structuredPatch = nonEmptyStructuredPatch(source)
    if (structuredPatch)
      return structuredPatch
    return cachedRenderValueForStrings(
      props.context,
      'fileEditDiff.hunks',
      [source.filePath, source.oldStr, source.newStr],
      () => rawDiffToHunks(source.oldStr, source.newStr),
    )
  })
  return (
    <DiffView
      hunks={hunks()}
      view={props.view}
      filePath={props.source.filePath}
      originalFile={props.source.originalFile}
      showLineNumbers={props.showLineNumbers}
      context={props.context}
    />
  )
}
