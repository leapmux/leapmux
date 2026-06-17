import type { ParsedUnifiedDiff, StructuredPatchHunk } from '../../../diff'
import { isObject, pickString } from '~/lib/jsonPick'
import { parseUnifiedDiffCached, rawDiffToHunks } from '../../../diff'

/**
 * Pure extraction of a Codex `fileChange` item's diff geometry, shared by the
 * renderer (`renderers/fileChange.tsx`) and the height estimator
 * (`heightMetrics.ts`) so both read the changes the same way -- no drift between
 * what mounts and what the row is pre-sized as.
 */

export function codexChangeKind(change: Record<string, unknown>): string {
  const kind = change.kind
  if (typeof kind === 'string')
    return kind
  if (isObject(kind) && typeof kind.type === 'string')
    return kind.type as string
  return ''
}

export function isSimpleAddChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'add' && typeof change.diff === 'string' && (change.diff as string).length > 0
}

export function isSimpleDeleteChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'delete'
}

export type CompletedFileChangeEntry
  = | { kind: 'diff', path: string, hunks: StructuredPatchHunk[] }
    | { kind: 'add', path: string, hunks: StructuredPatchHunk[] }

export function completedFileChangeEntries(
  changes: Array<Record<string, unknown>>,
  parsedDiffs: Map<Record<string, unknown>, ParsedUnifiedDiff | null>,
): CompletedFileChangeEntry[] {
  return changes.flatMap((change): CompletedFileChangeEntry[] => {
    const path = pickString(change, 'path')
    const diffText = pickString(change, 'diff')
    const parsed = parsedDiffs.get(change) ?? null
    if (parsed) {
      return [{ kind: 'diff', path, hunks: parsed.hunks }]
    }
    if (isSimpleAddChange(change)) {
      return [{ kind: 'add', path, hunks: rawDiffToHunks('', diffText) }]
    }
    return []
  })
}

export interface FileChangeShape {
  changes: Array<Record<string, unknown>>
  parsedDiffs: Map<Record<string, unknown>, ParsedUnifiedDiff | null>
  simpleAdd: Record<string, unknown> | null
  simpleAddPath: string
  simpleAddContent: string
  simpleDelete: Record<string, unknown> | null
  simpleDeletePath: string
  /**
   * The single change when this is a one-file UPDATE with a parsed diff, else
   * null -- the in-progress simple-header case, mirroring simpleAdd/simpleDelete so
   * the renderer stays a pure field-consumer instead of re-deriving the gate and
   * re-doing the parsed-diff lookup. `simpleUpdateDiff` is non-null exactly when
   * `simpleUpdate` is set (they are computed together).
   */
  simpleUpdate: Record<string, unknown> | null
  simpleUpdatePath: string
  simpleUpdateDiff: ParsedUnifiedDiff | null
}

export function buildFileChangeShape(item: Record<string, unknown>): FileChangeShape {
  // Array.isArray rather than `|| []`: a truthy non-array `changes` (a malformed
  // payload) would slip past `|| []` and then throw on the `for...of` below --
  // inside the height-estimate memo. Filter to OBJECT elements too: a `[null]` /
  // `[5]` array passes Array.isArray, and reading `change.diff`/`change.kind` off a
  // non-object element throws the same way (the sibling acp/pi fileEdit extractors
  // already `if (!isObject(...)) continue` per element). Keeps a malformed element
  // from faulting the estimator instead of just mis-sizing the row.
  const changes = (Array.isArray(item.changes) ? item.changes : []).filter(isObject) as Array<Record<string, unknown>>
  const parsedDiffs = new Map<Record<string, unknown>, ParsedUnifiedDiff | null>()
  for (const change of changes) {
    const diffText = typeof change.diff === 'string' ? change.diff : ''
    parsedDiffs.set(change, diffText ? parseUnifiedDiffCached(diffText) : null)
  }
  const simpleAdd = changes.length === 1 && isSimpleAddChange(changes[0]) ? changes[0] : null
  const simpleDelete = changes.length === 1 && isSimpleDeleteChange(changes[0]) ? changes[0] : null
  // A one-file update whose diff parsed: the third single-change variant, gated and
  // looked up HERE so the renderer doesn't re-derive it. Mutually exclusive with
  // simpleAdd/simpleDelete by kind ('update' vs 'add'/'delete').
  const onlyChange = changes.length === 1 ? changes[0] : null
  const onlyChangeDiff = onlyChange ? parsedDiffs.get(onlyChange) ?? null : null
  const simpleUpdate = onlyChange && codexChangeKind(onlyChange) === 'update' && onlyChangeDiff ? onlyChange : null
  return {
    changes,
    parsedDiffs,
    simpleAdd,
    simpleAddPath: simpleAdd ? pickString(simpleAdd, 'path') : '',
    simpleAddContent: simpleAdd ? pickString(simpleAdd, 'diff') : '',
    simpleDelete,
    simpleDeletePath: simpleDelete ? pickString(simpleDelete, 'path') : '',
    simpleUpdate,
    simpleUpdatePath: simpleUpdate ? pickString(simpleUpdate, 'path') : '',
    simpleUpdateDiff: simpleUpdate ? onlyChangeDiff : null,
  }
}
