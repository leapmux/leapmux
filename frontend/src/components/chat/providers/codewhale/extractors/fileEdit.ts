import type { FileChangeEntry, FileChangeOperation, FileEditDiff } from '../../../model/fileEditDiff'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickFirstString, pickObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { fileEditDiffFromOldNew, fileEditDiffFromUnifiedPatch, fileEditDiffFromWholeFile, fileEditDiffsFromChanges } from '../../../model/fileEditDiff'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS } from '../../toolInputKeys'
import { CODEWHALE_FILE_OUTCOME, CODEWHALE_MUTATION, CODEWHALE_RESULT_METADATA } from '../protocol'

/** The operation each `mutation.files[].outcome` word states. */
const OUTCOME_OPERATIONS: ReadonlyMap<string, FileChangeOperation> = new Map<string, FileChangeOperation>([
  [CODEWHALE_FILE_OUTCOME.Created, 'add'],
  [CODEWHALE_FILE_OUTCOME.Updated, 'edit'],
  [CODEWHALE_FILE_OUTCOME.Deleted, 'delete'],
])

/** The prefix a unified-diff header puts before a path, and the name of an absent side. */
const DIFF_SIDE_PREFIX = /^[ab]\//
const DEV_NULL = '/dev/null'

/** A header path without its `a/` or `b/` side prefix, or `''` for `/dev/null`. */
function headerPath(text: string): string {
  const path = text.trim().split('\t')[0] ?? ''
  return path === DEV_NULL ? '' : path.replace(DIFF_SIDE_PREFIX, '')
}

/** A path with no leading `./` or `/`, the form two spellings of one path share. */
function comparablePath(path: string): string {
  return path.replace(/^(?:\.\/|\/)+/, '')
}

/**
 * The hunks of one file, found by its path.
 *
 * The runtime writes the header from the path the call REQUESTED, so an absolute path
 * becomes `a//abs/path`, and the header and the file list agree. A path the two spell
 * differently still finds its hunks through the comparable form.
 */
function sectionFor(sections: Map<string, string>, path: string): string | undefined {
  const exact = sections.get(path)
  if (exact !== undefined)
    return exact
  const wanted = comparablePath(path)
  for (const [key, body] of sections) {
    if (comparablePath(key) === wanted)
      return body
  }
  return undefined
}

/**
 * The per-file sections of one combined unified diff, by path.
 *
 * The runtime states a multi-file change as ONE diff text: a `--- a/x` and `+++ b/x`
 * header pair before each file's hunks. The shared parser reads one file's hunks and
 * would take the next file's `--- a/y` header for a deleted line, so the text is split
 * at each header pair first. The new side's path keys a section, and the old side's
 * keys a deletion, whose new side is `/dev/null`.
 */
export function codewhaleDiffSections(diff: string): Map<string, string> {
  const sections = new Map<string, string>()
  const lines = diff.split('\n')
  let path = ''
  let body: string[] = []
  const flush = () => {
    if (path && body.length > 0)
      sections.set(path, body.join('\n'))
    body = []
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? ''
    const next = lines[i + 1] ?? ''
    if (line.startsWith('--- ') && next.startsWith('+++ ')) {
      flush()
      path = headerPath(next.slice(4)) || headerPath(line.slice(4))
      i++
      continue
    }
    if (line.startsWith('diff --git ')) {
      flush()
      path = ''
      continue
    }
    if (path)
      body.push(line)
  }
  flush()
  return sections
}

/**
 * The change a file tool LANDED, from the `mutation` record of its result.
 *
 * `files` states each file and what happened to it, `renames` states each move, and
 * `diff` holds every file's hunks in one text. Null when the result carries no record,
 * or a record that names no file -- a tool that states nothing, rather than one that
 * changed nothing.
 */
export function codewhaleMutationChanges(metadata: Record<string, unknown>): FileEditDiff[] | null {
  const mutation = pickObject(metadata, CODEWHALE_RESULT_METADATA.Mutation)
  if (!mutation)
    return null
  const sections = codewhaleDiffSections(pickString(mutation, CODEWHALE_MUTATION.Diff))
  const entries: FileChangeEntry[] = []
  const renames = mutation[CODEWHALE_MUTATION.Renames]
  for (const rename of Array.isArray(renames) ? renames.filter(isObject) : []) {
    const to = pickString(rename, CODEWHALE_MUTATION.To)
    const from = pickString(rename, CODEWHALE_MUTATION.From)
    if (to)
      entries.push({ filePath: to, ...(from ? { previousPath: from } : {}), operation: 'move' })
  }
  const files = mutation[CODEWHALE_MUTATION.Files]
  for (const file of Array.isArray(files) ? files.filter(isObject) : []) {
    const filePath = pickString(file, CODEWHALE_MUTATION.Path)
    if (!filePath)
      continue
    const patch = sectionFor(sections, filePath)
    entries.push({
      filePath,
      operation: OUTCOME_OPERATIONS.get(pickString(file, CODEWHALE_MUTATION.Outcome)) ?? 'edit',
      ...(patch !== undefined ? { patch } : {}),
    })
  }
  if (entries.length === 0)
    return null
  // A file the record lists with no hunks still states its operation, so the walk keeps
  // an add, a delete and a move. It drops only an `edit` that states no change.
  const changes = fileEditDiffsFromChanges(entries)
  return changes.length > 0 ? changes : entries.map(entry => ({ ...fileEditDiffFromOldNew(entry.filePath, '', ''), operation: entry.operation }))
}

/** The substitutions an `edit` call lists, one diff for each, in the order it lists them. */
function editChanges(filePath: string, args: Record<string, unknown>): FileEditDiff[] {
  const edits = Array.isArray(args.edits) ? args.edits.filter(isObject) : []
  if (edits.length > 0)
    return edits.map(edit => fileEditDiffFromOldNew(filePath, pickString(edit, 'oldText'), pickString(edit, 'newText')))
  // The legacy `edit_file` spelling states one substitution at the top level.
  const oldStr = pickFirstString(args, TOOL_OLD_TEXT_KEYS)
  const newStr = pickFirstString(args, TOOL_NEW_TEXT_KEYS)
  return [fileEditDiffFromOldNew(filePath, oldStr ?? '', newStr ?? '')]
}

/**
 * The files an `apply_patch` call asks to change.
 *
 * The tool takes a unified diff under `patch` -- for the one file `path` states, or
 * with a header pair before each file -- or whole-file replacements under `replace`
 * and its deprecated alias `changes`. A `*** Begin Patch` envelope is not a format the
 * tool accepts, but a model sends it, and the shared reader states what it asked for.
 */
function patchChanges(args: Record<string, unknown>): FileEditDiff[] {
  const replacements = [args.replace, args.changes].flatMap(value => Array.isArray(value) ? value.filter(isObject) : [])
  const replaced = replacements.flatMap((entry) => {
    const filePath = pickString(entry, 'path')
    return filePath ? [fileEditDiffFromWholeFile(filePath, pickString(entry, 'content'), 'add')] : []
  })
  const patch = pickString(args, 'patch')
  if (!patch)
    return replaced
  const envelope = applyPatchFileChanges(patch)
  if (envelope)
    return [...envelope, ...replaced]
  const sections = codewhaleDiffSections(patch)
  if (sections.size > 0) {
    const patched = [...sections].flatMap(([filePath, body]) => {
      const diff = fileEditDiffFromUnifiedPatch(filePath, body)
      return diff ? [diff] : []
    })
    return [...patched, ...replaced]
  }
  const filePath = pickString(args, 'path')
  const single = filePath ? fileEditDiffFromUnifiedPatch(filePath, patch) : null
  return single ? [single, ...replaced] : replaced
}

/**
 * The change a file tool ASKS for, read from its arguments before any result lands.
 *
 * `operation` is the facade's word when the `File` tool states one, and the tool's own
 * name otherwise. An empty list states that the arguments name no file, which the
 * caller reads as "not a file change".
 */
export function codewhaleRequestedChanges(toolName: string, args: Record<string, unknown>): FileEditDiff[] {
  const action = toolName === CODEWHALE_TOOL.File ? pickString(args, 'action') : ''
  if (toolName === CODEWHALE_TOOL.ApplyPatch || action === 'patch')
    return patchChanges(args)
  const filePath = pickFirstString(args, TOOL_FILE_PATH_KEYS)
  if (!filePath)
    return []
  if (toolName === CODEWHALE_TOOL.Write || toolName === CODEWHALE_TOOL.WriteFile || action === 'write')
    return [fileEditDiffFromWholeFile(filePath, pickString(args, 'content'), 'add')]
  return editChanges(filePath, args)
}
