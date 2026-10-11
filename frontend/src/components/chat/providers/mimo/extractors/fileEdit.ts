import type { FileChangeEntry, FileEditDiff } from '../../../model/fileEditDiff'
import type { MiMoToolPart } from './toolCommon'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { isObject, pickFirstString, pickObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { fileEditDiffFromUnifiedPatch, fileEditDiffsFromChanges, fileEditHasDiff } from '../../../model/fileEditDiff'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS } from '../../toolInputKeys'

/**
 * The changes one MiMo file tool ASKED for, from its arguments alone.
 *
 * Five tools change files, and each states the change in its own shape:
 *
 *   - `edit` states one replacement: `file_path`, `old_string`, `new_string`.
 *   - `multiedit` states several replacements in one file, under `edits`.
 *   - `write` states the file's whole new body, under `content`.
 *   - `apply_patch` states a `*** Begin Patch` envelope, under `patch_text`.
 *   - `notebook_edit` states one cell's new source, under `new_source`.
 *
 * An empty list is the answer for arguments that name no file, and the caller then
 * degrades the row rather than heading it with the operation word and no file.
 */
export function mimoRequestedChanges(tool: string, input: Record<string, unknown>): FileEditDiff[] {
  const filePath = pickFirstString(input, TOOL_FILE_PATH_KEYS) ?? ''
  switch (tool) {
    case MIMO_TOOL.ApplyPatch:
      return applyPatchFileChanges(pickString(input, 'patch_text')) ?? []
    case MIMO_TOOL.MultiEdit: {
      if (!filePath || !Array.isArray(input.edits))
        return []
      return input.edits.filter(isObject).map(edit => ({
        filePath,
        operation: 'edit' as const,
        oldStr: pickFirstString(edit, TOOL_OLD_TEXT_KEYS) ?? '',
        newStr: pickFirstString(edit, TOOL_NEW_TEXT_KEYS) ?? '',
        structuredPatch: null,
      }))
    }
    case MIMO_TOOL.NotebookEdit: {
      const notebook = pickString(input, 'notebook_path')
      return notebook ? [{ filePath: notebook, operation: 'edit', oldStr: '', newStr: pickString(input, 'new_source'), structuredPatch: null }] : []
    }
    case MIMO_TOOL.Write:
      return filePath ? [{ filePath, operation: 'add', oldStr: '', newStr: pickString(input, 'content'), structuredPatch: null }] : []
    default:
      return filePath
        ? [{
            filePath,
            operation: 'edit',
            oldStr: pickFirstString(input, TOOL_OLD_TEXT_KEYS) ?? '',
            newStr: pickFirstString(input, TOOL_NEW_TEXT_KEYS) ?? '',
            structuredPatch: null,
          }]
        : []
  }
}

/** The operation word of one `apply_patch` file entry. */
function patchOperation(type: string, movePath: string): FileChangeEntry['operation'] {
  if (movePath)
    return 'move'
  switch (type) {
    case 'add':
      return 'add'
    case 'delete':
      return 'delete'
    default:
      return 'edit'
  }
}

/**
 * The diff one `edit` states in its metadata: `filediff.patch` first, then `diff`,
 * on the file `filediff.file` gives, else `filepath`, else `requestedPath`. Null when
 * neither field holds a diff that changes a line.
 */
function statedDiff(metadata: Record<string, unknown>, requestedPath: string): FileEditDiff | null {
  const filediff = pickObject(metadata, 'filediff')
  const diffPath = pickString(filediff, 'file') || pickString(metadata, 'filepath') || requestedPath
  for (const patch of [pickString(filediff, 'patch'), pickString(metadata, 'diff')]) {
    const source = patch ? fileEditDiffFromUnifiedPatch(diffPath, patch) : null
    if (source && fileEditHasDiff(source))
      return source
  }
  return null
}

/**
 * The changes one finished MiMo file tool LANDED, from its metadata, or null when
 * the metadata states none.
 *
 * MiMo reports each landed change as a unified diff, in the shape of its tool:
 *
 *   - `apply_patch` states each file under `metadata.files`.
 *   - `edit` states `metadata.filediff.patch` and `metadata.diff`.
 *   - `write` and `notebook_edit` state `metadata.diff`.
 *   - `multiedit` runs `edit` once for each replacement, and states the metadata of
 *     each run under `metadata.results`, with no diff of its own.
 *
 * Each diff is the change as it reached the file, which can differ from the
 * arguments: an edit that MiMo matched with its fuzzy replacers states the text it
 * found, not the text the model sent.
 */
export function mimoLandedChanges(part: MiMoToolPart, requestedPath: string): FileEditDiff[] | null {
  const metadata = part.metadata
  if (Array.isArray(metadata.files)) {
    const changes = fileEditDiffsFromChanges(metadata.files.filter(isObject).flatMap((entry): FileChangeEntry[] => {
      const oldPath = pickString(entry, 'filePath')
      const movePath = pickString(entry, 'movePath')
      const target = movePath || oldPath
      if (!target)
        return []
      const patch = pickString(entry, 'patch') || pickString(entry, 'diff')
      return [{
        filePath: target,
        ...(movePath ? { previousPath: oldPath } : {}),
        operation: patchOperation(pickString(entry, 'type'), movePath),
        ...(patch ? { patch } : {}),
      }]
    }))
    if (changes.length > 0)
      return changes
  }
  if (part.tool === MIMO_TOOL.MultiEdit && Array.isArray(metadata.results)) {
    // Each replacement's own diff, in the order MiMo applied them. A run that states
    // no diff is skipped rather than drawn as an empty change.
    const changes = metadata.results.filter(isObject).flatMap(result => statedDiff(result, requestedPath) ?? [])
    return changes.length > 0 ? changes : null
  }
  const source = statedDiff(metadata, requestedPath)
  if (!source)
    return null
  return [part.tool === MIMO_TOOL.Write ? { ...source, operation: metadata.exists === true ? 'edit' : 'add' } : source]
}
