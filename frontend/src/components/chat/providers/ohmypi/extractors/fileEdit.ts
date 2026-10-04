import type { FileChangeOperation, FileEditDiff } from '../../../model/fileEditDiff'
import { isObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'

/** The four hex digits of the file snapshot the model last read, in a hashline header. */
const HASHLINE_TAG = /^[0-9A-F]{4}$/i
/** The operation keywords of a hashline section that are not line edits. */
const HASHLINE_REMOVE = 'REM'
const HASHLINE_MOVE = /^MV (.+)$/

/**
 * Whether a path closes a bracket that it did not open, which omp's grammar refuses in
 * a header (`header_path_has_orphan_bracket`).
 */
function hasOrphanBracket(path: string): boolean {
  let depth = 0
  for (const char of path) {
    if (char === '[') {
      depth++
    }
    else if (char === ']') {
      if (depth === 0)
        return true
      depth--
    }
  }
  return false
}

/** The `apply_patch` envelope markers that a model can mix into a hashline patch. */
const ENVELOPE_MARKERS = ['*** Begin Patch', '*** End Patch', '*** Abort'] as const

/**
 * A row without the bracketed envelope markers that open it.
 *
 * A model that mixes `apply_patch` framing into a hashline patch can put a marker in
 * brackets, alone (`[*** End Patch]`) or before a header
 * (`[*** Begin Patch] [src/a.ts#1A2B]`). omp removes those groups before it reads the
 * header (`unbracket_envelope_markers` in `hashline/input.rs`). Without that, the
 * marker itself reads as a `[PATH]` header of a file called `*** End Patch`.
 */
function withoutEnvelopeMarkers(line: string): string {
  let rest = line
  for (;;) {
    if (!rest.startsWith('['))
      return rest
    const inner = rest.slice(1).trimStart()
    const marker = ENVELOPE_MARKERS.find(candidate => inner.startsWith(candidate))
    if (marker === undefined)
      return rest
    let tail = inner.slice(marker.length).trimStart()
    if (tail.startsWith(']'))
      tail = tail.slice(1).trimStart()
    if (!tail)
      return marker
    rest = tail
  }
}

/**
 * The file of one hashline section header, or null for a row that is not one.
 *
 * omp's grammar (`crates/pi-edit/src/modes/hashline/tokenizer.rs`, `parse_header`)
 * takes `[PATH#TAG]`, where TAG is the four hex digits of the file snapshot that the
 * model last read, and `[PATH]`, for a file that the model did not read, such as a
 * new file. A path holds no `#` and no bracket that it does not open.
 */
function hashlineHeaderPath(row: string): string | null {
  const line = withoutEnvelopeMarkers(row)
  if (!line.startsWith('[') || !line.endsWith(']'))
    return null
  const body = line.slice(1, -1)
  const hash = body.lastIndexOf('#')
  const path = hash < 0 ? body : body.slice(0, hash)
  if (hash >= 0 && !HASHLINE_TAG.test(body.slice(hash + 1)))
    return null
  if (!path || path.includes('#') || hasOrphanBracket(path))
    return null
  return path
}

/**
 * The files a hashline patch asks to change, one entry each, in the order the patch
 * states them.
 *
 * A hashline patch addresses lines by the numbers a `read` printed, so it holds no
 * BEFORE side at all. Each entry therefore states the file and the operation, and no
 * content -- the same answer the shared request gives an edit whose arguments state
 * a file and no replacement text. The result states the change itself.
 */
export function ohMyPiHashlineChanges(input: string): FileEditDiff[] {
  const changes: FileEditDiff[] = []
  let current: FileEditDiff | undefined
  for (const raw of input.replace(/\r\n/g, '\n').split('\n')) {
    const line = raw.trim()
    const headerPath = hashlineHeaderPath(line)
    if (headerPath !== null) {
      current = { filePath: headerPath, operation: 'edit', oldStr: '', newStr: '', structuredPatch: null }
      changes.push(current)
      continue
    }
    if (!current)
      continue
    if (line === HASHLINE_REMOVE) {
      current.operation = 'delete'
      continue
    }
    const move = HASHLINE_MOVE.exec(line)
    if (move?.[1] !== undefined) {
      current.previousPath = current.filePath
      current.filePath = move[1]
      current.operation = 'move'
    }
  }
  return changes
}

/**
 * The files one `edit` or `apply_patch` call asks to change, from the patch text in
 * its `input` argument: an `apply_patch` envelope, else a hashline patch.
 *
 * Returns null when the call states no patch text, so the caller reads the other
 * edit modes -- `replace` states `{path, old_string, new_string}`, which the shared
 * request reads.
 */
export function ohMyPiPatchChanges(args: Record<string, unknown>): FileEditDiff[] | null {
  const input = pickString(args, 'input')
  if (!input.trim())
    return null
  return applyPatchFileChanges(input) ?? ohMyPiHashlineChanges(input)
}

/** omp's operation word for one file, in the model's vocabulary. */
function changeOperation(op: string, sourcePath: string): FileChangeOperation {
  if (op === 'create')
    return 'add'
  if (op === 'delete')
    return 'delete'
  if (sourcePath)
    return 'move'
  return 'edit'
}

/**
 * One file change as omp's result states it, or null when it states no snapshot.
 *
 * omp keeps the file's text before and after the edit (`oldText`, `newText`), which is
 * the source of truth; its `diff` field is its own numbered format, not a unified diff.
 * A create states no `oldText`, and a delete states no `newText`.
 */
function resultChange(entry: Record<string, unknown>, fallbackPath: string): FileEditDiff | null {
  const filePath = pickString(entry, 'path') || fallbackPath
  const oldText = entry.oldText
  const newText = entry.newText
  if (!filePath || (typeof oldText !== 'string' && typeof newText !== 'string'))
    return null
  const sourcePath = pickString(entry, 'sourcePath')
  return {
    filePath,
    ...(sourcePath && sourcePath !== filePath ? { previousPath: sourcePath } : {}),
    operation: changeOperation(pickString(entry, 'op'), sourcePath),
    oldStr: typeof oldText === 'string' ? oldText : '',
    newStr: typeof newText === 'string' ? newText : '',
    structuredPatch: null,
  }
}

/** What a file of a multi-file edit states in place of its diff when omp kept no snapshot of it. */
const PRUNED_SNAPSHOT_NOTICE = 'omp kept no snapshot of this file, so no diff is available.'

/**
 * One file of a multi-file edit whose snapshot omp did not keep, or null for an entry
 * that states no file.
 *
 * omp drops both texts of each later file once the snapshots of one edit pass its
 * budget (`capPerFileSnapshots` in `edit/index.ts`), and of an oversized file. The
 * file still changed, so it keeps its place in the list, with a notice for its diff.
 */
function prunedChange(entry: Record<string, unknown>): FileEditDiff | null {
  const filePath = pickString(entry, 'path')
  if (!filePath)
    return null
  const sourcePath = pickString(entry, 'sourcePath')
  return {
    filePath,
    ...(sourcePath && sourcePath !== filePath ? { previousPath: sourcePath } : {}),
    operation: changeOperation(pickString(entry, 'op'), sourcePath),
    oldStr: '',
    newStr: '',
    structuredPatch: null,
    notice: PRUNED_SNAPSHOT_NOTICE,
  }
}

/**
 * The changes one finished edit states in its `details`: one per file of a multi-file
 * edit (`perFileResults`), else the one file the top-level fields state.
 *
 * A file of a multi-file edit whose snapshot omp did not keep stays in the list with a
 * notice, so the list states every file the edit changed. A single-file edit with no
 * snapshot returns an empty list, and the caller then draws the result text, which is
 * omp's only statement of that change.
 */
export function ohMyPiResultChanges(details: Record<string, unknown>, fallbackPath: string): FileEditDiff[] {
  const perFile = details.perFileResults
  if (Array.isArray(perFile) && perFile.length > 0) {
    return perFile.flatMap((entry) => {
      if (!isObject(entry) || entry.isError === true)
        return []
      const change = resultChange(entry, '') ?? prunedChange(entry)
      return change ? [change] : []
    })
  }
  const change = resultChange(details, fallbackPath)
  return change ? [change] : []
}

/** The file a `write` call wrote: its whole content, added. */
export function ohMyPiWriteChange(args: Record<string, unknown>, resolvedPath: string): FileEditDiff | null {
  const filePath = resolvedPath || pickString(args, 'path')
  if (!filePath)
    return null
  return { filePath, operation: 'add', oldStr: '', newStr: pickString(args, 'content'), structuredPatch: null }
}
