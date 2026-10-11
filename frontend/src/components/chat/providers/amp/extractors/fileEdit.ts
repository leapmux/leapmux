import type { FileChangeEntry, FileChangeOperation, FileEditDiff } from '../../../model/fileEditDiff'
import { isObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { fileEditDiffFromOldNew, fileEditDiffFromUnifiedPatch, fileEditDiffFromWholeFile, fileEditDiffsFromChanges } from '../../../model/fileEditDiff'

/**
 * The file operations of Amp's three file tools.
 *
 *   apply_patch  {patchText}                 -> {summary, files:[{uri, type, additions, deletions, diff}]}
 *   edit_file    {path, old_str, new_str, replace_all?} -> {diff, lineRange}
 *   create_file  {path, content}              -> a confirmation
 *
 * `apply_patch` states each file's unified diff in its result, which is what landed.
 * The other two state the change in their arguments, and the result confirms it.
 */

/** The operation of one `apply_patch` result entry. Amp spells an edit `update`. */
function patchOperation(type: string): FileChangeOperation {
  switch (type) {
    case 'add':
      return 'add'
    case 'delete':
      return 'delete'
    case 'move':
      return 'move'
    default:
      return 'edit'
  }
}

/**
 * The path of a `file:` URI, or the text unchanged when it is not one.
 *
 * On Windows the URI path starts with a slash before the drive letter
 * (`file:///C:/work/a.go`), which the path does not have.
 */
export function ampFilePathFromUri(uri: string): string {
  if (!uri.startsWith('file:'))
    return uri
  let path: string
  try {
    path = decodeURIComponent(new URL(uri).pathname)
  }
  catch {
    return uri
  }
  return /^\/[A-Z]:/i.test(path) ? path.slice(1) : path
}

/** The changes one `apply_patch` call asked for, or null when its patch text does not parse. */
export function ampPatchRequestChanges(args: Record<string, unknown>): FileEditDiff[] | null {
  const patch = pickString(args, 'patchText')
  return patch ? applyPatchFileChanges(patch) : null
}

/**
 * The changes that one `apply_patch` call landed, from its result record, or null for a
 * result that is not that record.
 *
 * Amp leaves the diffs out when they are larger than a megabyte together, and an entry
 * then states its operation alone.
 */
export function ampPatchResultChanges(text: string): FileEditDiff[] | null {
  let record: unknown
  try {
    record = JSON.parse(text)
  }
  catch {
    return null
  }
  if (!isObject(record) || !Array.isArray(record.files))
    return null
  const entries: FileChangeEntry[] = record.files.filter(isObject).flatMap((file) => {
    const filePath = ampFilePathFromUri(pickString(file, 'uri'))
    if (!filePath)
      return []
    const patch = pickString(file, 'diff')
    return [{ filePath, operation: patchOperation(pickString(file, 'type')), ...(patch ? { patch } : {}) }]
  })
  return fileEditDiffsFromChanges(entries)
}

/** The change one `edit_file` call asked for. */
export function ampEditFileChange(args: Record<string, unknown>): FileEditDiff | null {
  const path = pickString(args, 'path')
  if (!path)
    return null
  return { ...fileEditDiffFromOldNew(path, pickString(args, 'old_str'), pickString(args, 'new_str')), operation: 'edit' }
}

/**
 * The change one `edit_file` call landed.
 *
 * Amp's result states the edit as a unified diff with the context around it, which is
 * what the row draws when it parses. The requested change stands in for a diff that
 * does not parse: Amp applied that exact change, or the call failed.
 */
export function ampEditFileResultChange(text: string, requested: FileEditDiff | null): FileEditDiff | null {
  const path = requested?.filePath ?? ''
  let record: unknown
  try {
    record = JSON.parse(text)
  }
  catch {
    record = undefined
  }
  const diff = isObject(record) ? pickString(record, 'diff') : ''
  const patched = path && diff ? fileEditDiffFromUnifiedPatch(path, stripDiffFence(diff)) : null
  return patched ? { ...patched, operation: 'edit' } : requested
}

/** The body of a fenced diff block, or the text unchanged when it is not fenced. */
function stripDiffFence(text: string): string {
  const fenced = /^```[\w-]*\n([\s\S]*?)\n```\s*$/.exec(text.trim())
  return fenced?.[1] ?? text
}

/** The file one `create_file` call writes. */
export function ampCreateFileChange(args: Record<string, unknown>): FileEditDiff | null {
  const path = pickString(args, 'path')
  return path ? fileEditDiffFromWholeFile(path, pickString(args, 'content'), 'add') : null
}
