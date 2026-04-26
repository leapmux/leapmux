import type { FileEditDiffSource } from '../../results/fileEditDiff'
import { isObject, pickFirstString, pickString } from '~/lib/jsonPick'
import { ACP_TOOL_KIND } from '~/types/toolMessages'

/**
 * Build a FileEditDiffSource from an ACP `tool_call`/`tool_call_update`
 * `content` array, picking the first `{ type: 'diff', path, oldText, newText }`
 * entry. Returns null when no diff entry is present.
 */
export function acpFileEditFromToolCallContent(
  content: unknown,
): FileEditDiffSource | null {
  if (!Array.isArray(content))
    return null
  for (const entry of content) {
    if (!isObject(entry))
      continue
    const obj = entry as Record<string, unknown>
    if (obj.type !== 'diff')
      continue
    const path = pickString(obj, 'path')
    const oldText = pickString(obj, 'oldText')
    const newText = pickString(obj, 'newText')
    if (!path && oldText === '' && newText === '')
      return null
    return {
      filePath: path,
      structuredPatch: null,
      oldStr: oldText,
      newStr: newText,
    }
  }
  return null
}

/**
 * Build a FileEditDiffSource fallback from an ACP tool_call's `rawInput`. Used
 * when the corresponding tool_call_update arrived without an embedded diff but
 * the original input carries enough information to synthesize one. Recognized
 * shapes (best-effort across ACP-using agents):
 *
 * - edit-style: `{ filePath/path, oldText/oldString/old_string, newText/newString/new_string }`
 * - write-style: `{ filePath/path, content }` (treated as a new-file write)
 *
 * Returns null when the input matches neither shape.
 */
export function acpFileEditFromToolCallRawInput(
  kind: string | undefined,
  rawInput: Record<string, unknown> | null | undefined,
): FileEditDiffSource | null {
  if (!rawInput)
    return null

  const filePath = pickFirstString(rawInput, ['filePath', 'path', 'file_path']) ?? ''
  if (!filePath)
    return null

  const oldStr = pickFirstString(rawInput, ['oldText', 'oldString', 'old_string'])
  const newStr = pickFirstString(rawInput, ['newText', 'newString', 'new_string'])
  if (oldStr !== undefined || newStr !== undefined) {
    return {
      filePath,
      structuredPatch: null,
      oldStr: oldStr ?? '',
      newStr: newStr ?? '',
    }
  }

  // write-style fallback: only meaningful for the `edit`/`write` kinds (or
  // unknown kind), to avoid mistaking a `read` rawInput for a write payload.
  if (kind === ACP_TOOL_KIND.READ || kind === ACP_TOOL_KIND.SEARCH || kind === ACP_TOOL_KIND.EXECUTE)
    return null
  const content = pickFirstString(rawInput, ['content'])
  if (content === undefined)
    return null
  return {
    filePath,
    structuredPatch: null,
    oldStr: '',
    newStr: content,
  }
}
