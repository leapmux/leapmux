import type { FileEditDiff } from '../../../model/fileEditDiff'
import { isObject, pickString } from '~/lib/jsonPick'

/** Read the applied native hunks. Requested text supplies no proof of an applied edit. */
export function deepseekHarnessAppliedChanges(meta: Record<string, unknown> | undefined): FileEditDiff[] | undefined {
  if (!Array.isArray(meta?.diffs))
    return undefined
  const changes: FileEditDiff[] = []
  for (const diff of meta.diffs) {
    if (!isObject(diff) || typeof diff.path !== 'string' || diff.path === '' || typeof diff.newText !== 'string'
      || (diff.oldText !== null && typeof diff.oldText !== 'string')) {
      return undefined
    }
    changes.push({
      filePath: diff.path,
      operation: diff.oldText === null ? 'add' : 'edit',
      oldStr: diff.oldText ?? '',
      newStr: diff.newText,
      structuredPatch: null,
    })
  }
  return changes
}

/** Native creates omit hunks. Their committed operation permits the native argument fallback. */
export function deepseekHarnessCreatedChange(meta: Record<string, unknown> | undefined, args: Record<string, unknown>): FileEditDiff | undefined {
  const filePath = pickString(args, 'file_path')
  return meta?.operation === 'create' && filePath && typeof args.content === 'string'
    ? { filePath, operation: 'add', oldStr: '', newStr: args.content, structuredPatch: null }
    : undefined
}
