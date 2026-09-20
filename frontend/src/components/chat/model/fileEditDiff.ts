import type { StructuredPatchHunk } from '../diff/diffTypes'
import { formatUnifiedDiffText, rawDiffToHunks } from '../diff/diffBuilder'
import { parseUnifiedDiffCached } from '../diff/unifiedDiffParser'

/**
 * What one file change DID.
 *
 * `FileEditDiff` states it for a single-file call and `FileChangeEntry` for one file
 * of a multi-file change, and the walk that builds the second from the first compares
 * against `edit`. One name is what keeps a fifth operation from reaching one of them
 * and not the other.
 */
export type FileChangeOperation = 'add' | 'edit' | 'delete' | 'move'

/**
 * Where one file edit's hunks come from: a patch the provider pre-computed (Claude's
 * `tool_use_result`), or the two SIDES it stated, which a tool_use input carries when
 * the result message did not.
 *
 * Exactly one. {@link fileEditDiffHunks} prefers the patch and DROPS the sides, so a
 * source that carried both drew the patch and silently ignored whatever the sides
 * said -- and {@link fileEditHasDiff} answered from the sides even when the patch was
 * empty, so the two disagreed about whether the row had anything to draw.
 */
export type FileEditContent
  = | { structuredPatch: StructuredPatchHunk[], oldStr?: never, newStr?: never }
    | { structuredPatch?: null, oldStr: string, newStr: string }

/**
 * One file's edit, as every provider normalizes it.
 *
 * `originalFile` lets the diff view expand gap context past the hunks.
 */
export type FileEditDiff = FileEditBase & FileEditContent

export interface FileEditBase {
  filePath: string
  previousPath?: string
  operation?: FileChangeOperation
  showLineNumbers?: boolean
  notice?: string
  originalFile?: string
}

/** The BEFORE side of an edit, or `''` for one stated as a pre-computed patch. */
export function fileEditOldStr(source: FileEditDiff): string {
  return source.oldStr ?? ''
}

/** The AFTER side of an edit, or `''` for one stated as a pre-computed patch. */
export function fileEditNewStr(source: FileEditDiff): string {
  return source.newStr ?? ''
}

/**
 * The content half of an edit, chosen the way {@link fileEditDiffHunks} reads it.
 *
 * A provider that holds both a patch and two sides calls this rather than picking one
 * by hand: the patch wins when it carries hunks, because that is what the row draws.
 */
export function fileEditContent(patch: StructuredPatchHunk[] | null | undefined, oldStr: string, newStr: string): FileEditContent {
  return patch?.length ? { structuredPatch: patch } : { structuredPatch: null, oldStr, newStr }
}

const NO_NEWLINE_MARKER = '\\ No newline at end of file'

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

/**
 * Every hunk of a provider's structured patch that this build can draw.
 *
 * A malformed hunk is dropped, and the rest of the patch still draws. Rejecting the
 * WHOLE list for one bad hunk sent the row to `rawDiffToHunks(oldStr, newStr)`, and a
 * source built from hunks alone (`fileEditDiffFromHunks`) states both of those as the
 * empty string -- so one hunk a provider miscounted left a ten-hunk edit drawing
 * nothing at all.
 *
 * Returns null when the input is not a list, and when no hunk survived: the callers
 * read null as "this source carries no structured patch", which is the truth in both.
 */
export function normalizeStructuredPatchHunks(hunks: unknown): StructuredPatchHunk[] | null {
  if (!Array.isArray(hunks))
    return null
  let normalizedHunks: StructuredPatchHunk[] | undefined
  for (let i = 0; i < hunks.length; i++) {
    const normalized = normalizeStructuredPatchHunk(hunks[i])
    if (!normalized) {
      normalizedHunks ??= (hunks.slice(0, i) as StructuredPatchHunk[])
      continue
    }
    if (normalizedHunks) {
      normalizedHunks.push(normalized)
    }
    else if (normalized !== hunks[i]) {
      normalizedHunks = (hunks.slice(0, i) as StructuredPatchHunk[]).concat(normalized)
    }
  }
  const result = normalizedHunks ?? (hunks as StructuredPatchHunk[])
  // A list that HELD hunks and kept none carries no drawable patch, so it answers the
  // null an absent one answers. An EMPTY list stays empty: nothing was dropped, and a
  // zero-hunk patch is what the caller handed over.
  return hunks.length > 0 && result.length === 0 ? null : result
}

/**
 * One normalization per source object.
 *
 * `normalizeStructuredPatchHunks` walks EVERY LINE of every hunk -- a `startsWith`,
 * an inspection of the first character and a tally against the declared counts -- and
 * the answer is asked for five to seven times per reactive pass of one row: the
 * toolbar's `hasDiff`, the body's `Show`, the title's stats, the body's hunks, and the
 * copy text. A 600-line edit paid for all of it each time.
 *
 * The key is the SOURCE, which the model rebuilds whenever the row's revision changes, so
 * the entry expires exactly when the input can differ. That is the same rule
 * `copyableByRow` uses, rather than a second one that could drift.
 */
const nonEmptyPatchBySource = new WeakMap<FileEditDiff, StructuredPatchHunk[] | null>()

export function nonEmptyStructuredPatch(source: FileEditDiff): StructuredPatchHunk[] | null {
  const cached = nonEmptyPatchBySource.get(source)
  if (cached !== undefined)
    return cached
  const hunks = normalizeStructuredPatchHunks(source.structuredPatch)
  const answer = hunks && hunks.length > 0 ? hunks : null
  nonEmptyPatchBySource.set(source, answer)
  return answer
}

/**
 * Build a FileEditDiff for an edit whose unified diff has already
 * been parsed into structured hunks. Provider-neutral: used by Codex's
 * fileChange "modify" rows (hunks parsed from a unified diff) and Pi's
 * `edit` tool result (hunks parsed via parsePiNumberedDiff).
 */
export function fileEditDiffFromHunks(path: string, hunks: StructuredPatchHunk[]): FileEditDiff {
  // A patch this build cannot read leaves NO sides to fall back to, so the source
  // states an empty pair rather than a patch it would then have to guard at each read.
  return { filePath: path, ...fileEditContent(normalizeStructuredPatchHunks(hunks), '', '') }
}

/**
 * Does this source draw anything? A no-op edit -- equal sides and no patch -- draws
 * nothing, and it is still a perfectly good {@link FileEditDiff}.
 *
 * Ask THIS when you hold a source and want the content question. {@link fileEditHasDiff}
 * answers the same question, but it also narrows, and its false branch narrows to
 * `never` for a non-null argument. TypeScript cannot declare a guard whose TRUE branch
 * narrows and whose false branch says nothing, so a caller that wants to keep the value
 * and report "no change" must not use the guard.
 */
export function fileEditDrawsDiff(source: FileEditDiff): boolean {
  if (nonEmptyStructuredPatch(source))
    return true
  // A creation or deletion has one empty side.
  return fileEditOldStr(source) !== fileEditNewStr(source)
}

/**
 * Present AND drawable, as one guard, for a caller that discards the value otherwise.
 *
 * Read {@link fileEditDrawsDiff} before you add a caller: the false branch of this
 * guard narrows away the argument, so it suits `filter`, `pick` and `x ? [x] : []`,
 * and it does NOT suit a branch that goes on to read the source.
 */
export function fileEditHasDiff(source: FileEditDiff | null | undefined): source is FileEditDiff {
  return !!source && fileEditDrawsDiff(source)
}

/**
 * Result-side diff wins; otherwise fall back to the tool_use-side diff.
 * Returns null when neither has a renderable diff — callers can render the
 * return value directly without a separate `fileEditHasDiff` check.
 */
export function pickFileEditDiff(
  resultDiff: FileEditDiff | null | undefined,
  toolUseDiff: FileEditDiff | null | undefined,
): FileEditDiff | null {
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
export function fileEditDiffHunks(source: FileEditDiff): StructuredPatchHunk[] {
  return nonEmptyStructuredPatch(source) ?? rawDiffToHunks(fileEditOldStr(source), fileEditNewStr(source))
}

export function fileEditCopyableText(source: FileEditDiff): string {
  const { filePath, previousPath, operation } = source
  const diff = fileEditHasDiff(source) ? formatUnifiedDiffText(fileEditDiffHunks(source), source.filePath) : ''
  const metadata = previousPath && previousPath !== filePath
    ? `rename from ${previousPath}\nrename to ${filePath}`
    : !diff && operation === 'delete'
        ? `Deleted ${filePath}`
        : !diff && operation === 'add' ? `Created ${filePath}` : ''
  return [metadata, source.notice, diff].filter(Boolean).join('\n')
}

/** Preserve the proposed operation when no file content exists to construct a diff. */
export function requestedFileChangesCopyable(sources: FileEditDiff[]): string {
  if (sources.length === 0)
    return ''
  return ['Requested changes', ...sources.map((source) => {
    // The copy text words a REQUESTED operation itself ("Delete x"), so the operation
    // is pulled out here: the landed-change wording ("Deleted x") must not state it first.
    const { operation, ...rest } = source
    const diff = fileEditCopyableText(rest)
    if (diff)
      return diff
    const verb = operation === 'delete' ? 'Delete' : operation === 'add' ? 'Create' : 'Change'
    return `${verb} ${source.filePath}`
  })].join('\n\n')
}

/**
 * Build a diff for an edit the provider stated as its two sides.
 *
 * The named sibling of {@link fileEditDiffFromHunks} and
 * {@link fileEditDiffFromUnifiedPatch}.
 *
 * These four builders keep the exclusive halves apart, but they are not the only way
 * in: fourteen provider sites still spell the literal, so the SHAPE is what the type
 * would have to state before a row could no longer arrive with two sides in one field
 * and a patch in another.
 */
export function fileEditDiffFromOldNew(path: string, oldStr: string, newStr: string): FileEditDiff {
  return { filePath: path, structuredPatch: null, oldStr, newStr }
}

/**
 * Build a diff from a UNIFIED PATCH, or null when the text does not parse as one.
 *
 * Three call sites parse a patch here and then build the same source from its hunks:
 *
 *   - {@link fileEditDiffsFromChanges} below, for the `patch` of ONE file entry. Codex's
 *     `fileChange` items and the OpenCode family's `metadata.files` both arrive there.
 *   - OpenCode's `patch` ARGUMENT, which states the change the call asked for. Kilo
 *     registers the same extractor, so its tools reach this too.
 *   - OpenCode's `metadata.diff`, which states the change that landed.
 *
 * Returning null lets each of them fall back to its own plain-text branch.
 *
 * Two readers of a patch do NOT come here. Reasonix's `delete_range` and
 * `delete_symbol` answer a unified diff and call `parseUnifiedDiffCached` and
 * {@link fileEditDiffFromHunks} themselves; its edit RECEIPTS are another format, which
 * `reasonixEditReceipt` reads into two sides. And the `*** Begin Patch` envelope is not
 * a unified diff at all: `model/applyPatch.ts` reads it, for Copilot and ZCode both.
 */
export function fileEditDiffFromUnifiedPatch(path: string, patch: string): FileEditDiff | null {
  const parsed = parseUnifiedDiffCached(patch)
  return parsed ? fileEditDiffFromHunks(path, parsed.hunks) : null
}

/**
 * Build a diff for a WHOLE file: an add puts the content on the new side, a delete
 * puts it on the old one.
 *
 * A provider that sends a file's whole body rather than a patch states which of the
 * two happened, and reading it wrong inverts every line of the row.
 */
export function fileEditDiffFromWholeFile(path: string, content: string, operation: Extract<FileChangeOperation, 'add' | 'delete'>): FileEditDiff {
  return operation === 'add'
    ? { filePath: path, structuredPatch: null, oldStr: '', newStr: content, operation }
    : { filePath: path, structuredPatch: null, oldStr: content, newStr: '', operation }
}

/**
 * One file inside a multi-file change, in the shape every provider normalizes to.
 *
 * Codex sends `changes[]`, OpenCode and Kilo send `metadata.files[]`, and Copilot
 * sends structured `actions[]`. The three carry the same facts under three sets of
 * field names, so each provider renames its own fields into this and the walk below
 * is shared.
 */
export interface FileChangeEntry {
  filePath: string
  /** Where the file was before a move. */
  previousPath?: string
  operation: FileChangeOperation
  /** A unified patch for this file, when the provider sent one. */
  patch?: string
  /** The file's whole body, for an add or a delete that sent content instead. */
  content?: string
}

/**
 * Build one diff per file of a multi-file change.
 *
 * An entry that resolves to NO diff is kept when its operation states something on
 * its own -- a move states the rename, an add states the creation, a delete states
 * the removal -- and dropped only for an `edit` that changed nothing, which has
 * nothing left to show. Dropping a move used to hide a file the call had touched.
 */
export function fileEditDiffsFromChanges(entries: FileChangeEntry[]): FileEditDiff[] {
  return entries.flatMap((entry) => {
    const patched = entry.patch ? fileEditDiffFromUnifiedPatch(entry.filePath, entry.patch) : null
    const whole = !patched && entry.content !== undefined && (entry.operation === 'add' || entry.operation === 'delete')
      ? fileEditDiffFromWholeFile(entry.filePath, entry.content, entry.operation)
      : null
    const source = patched ?? whole ?? fileEditDiffFromOldNew(entry.filePath, '', '')
    if (!fileEditDrawsDiff(source) && entry.operation === 'edit')
      return []
    // The move's old path rides only when the entry stated one, so an entry without
    // one keeps no `previousPath` key rather than an explicitly undefined one.
    const renamed = { ...source, operation: entry.operation }
    return [entry.previousPath !== undefined ? { ...renamed, previousPath: entry.previousPath } : renamed]
  })
}
