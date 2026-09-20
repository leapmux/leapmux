import type { FileEditDiff } from '../../../model/fileEditDiff'
import { ACP_TOOL_KIND } from '~/generated/contracts/acp-protocol'
import { isObject, pickFirstString, pickString } from '~/lib/jsonPick'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS } from '../../toolInputKeys'

/**
 * Build a FileEditDiff from an ACP `tool_call`/`tool_call_update`
 * `content` array, picking the first `{ type: 'diff', path, oldText, newText }`
 * entry. Returns null when no diff entry is present.
 */
export function acpFileEditFromToolCallContent(
  content: unknown,
): FileEditDiff | null {
  if (!Array.isArray(content))
    return null
  for (const entry of content) {
    if (!isObject(entry))
      continue
    if (entry.type !== 'diff')
      continue
    const path = pickString(entry, 'path')
    const oldText = pickString(entry, 'oldText')
    const newText = pickString(entry, 'newText')
    // Skip empty diff entries; a later entry in the array may carry usable text.
    if (!path && !oldText && !newText)
      continue
    return {
      filePath: path,
      structuredPatch: null,
      oldStr: oldText,
      newStr: newText,
    }
  }
  return null
}

/** One substitution of an `edits` list, or null when the entry states neither side. */
function acpListedReplacement(filePath: string, entry: unknown): FileEditDiff | null {
  if (!isObject(entry))
    return null
  const oldStr = pickFirstString(entry, TOOL_OLD_TEXT_KEYS)
  const newStr = pickFirstString(entry, TOOL_NEW_TEXT_KEYS)
  if (oldStr === undefined && newStr === undefined)
    return null
  return { filePath, structuredPatch: null, oldStr: oldStr ?? '', newStr: newStr ?? '' }
}

/**
 * Every change an ACP tool_call's `rawInput` states, in the order the call sent them.
 *
 * Used when the matching tool_call_update arrived without an embedded diff but the
 * original input carries enough to synthesize one. Recognized shapes (best-effort
 * across ACP-using agents):
 *
 * - multi-edit: `{ filePath/path, edits: [{ oldText/oldString/old_string, newText/… }] }`
 * - edit-style: `{ filePath/path, oldText/oldString/old_string, newText/newString/new_string }`
 * - write-style: `{ filePath/path, content }` (treated as a new-file write)
 *
 * A LIST, because one call can ask for several substitutions in one file and no
 * single pair describes them. The `edits` spelling is the one three agents in this
 * repository send -- Claude's `MultiEdit`, Reasonix's `multi_edit` and Pi's `edit` --
 * and each entry states the two sides under the same keys the root pair uses, which
 * is why `TOOL_OLD_TEXT_KEYS` and `TOOL_NEW_TEXT_KEYS` read both. The FILE stays at
 * the root in all three, so an entry never states one of its own.
 *
 * The listed substitutions come first and the root pair after them, which is the
 * order Pi normalizes its own arguments into. The write-style body answers only when
 * neither stated anything, so a call that carries both is read as the edit it is.
 *
 * Empty for an input that matches no shape, and for one that states no file.
 */
export function acpFileEditsFromToolCallRawInput(
  kind: string | undefined,
  rawInput: Record<string, unknown> | null | undefined,
): FileEditDiff[] {
  if (!rawInput)
    return []

  const filePath = pickFirstString(rawInput, TOOL_FILE_PATH_KEYS) ?? ''
  if (!filePath)
    return []

  const changes: FileEditDiff[] = Array.isArray(rawInput.edits)
    ? rawInput.edits.flatMap((entry) => {
        const listed = acpListedReplacement(filePath, entry)
        return listed ? [listed] : []
      })
    : []

  const oldStr = pickFirstString(rawInput, TOOL_OLD_TEXT_KEYS)
  const newStr = pickFirstString(rawInput, TOOL_NEW_TEXT_KEYS)
  if (oldStr !== undefined || newStr !== undefined)
    changes.push({ filePath, structuredPatch: null, oldStr: oldStr ?? '', newStr: newStr ?? '' })
  if (changes.length > 0)
    return changes

  // write-style fallback: only meaningful for the `edit`/`write` kinds (or
  // unknown kind), to avoid mistaking a `read` rawInput for a write payload.
  if (kind === ACP_TOOL_KIND.Read || kind === ACP_TOOL_KIND.Search || kind === ACP_TOOL_KIND.Execute)
    return []
  const content = pickFirstString(rawInput, ['content'])
  if (content === undefined)
    return []
  return [{ filePath, structuredPatch: null, oldStr: '', newStr: content }]
}

/**
 * The FIRST change an ACP tool_call's `rawInput` states, or null when it states none.
 *
 * For a caller that draws one change and no more. A multi-edit states several, so
 * take {@link acpFileEditsFromToolCallRawInput} wherever the whole list can draw.
 */
export function acpFileEditFromToolCallRawInput(
  kind: string | undefined,
  rawInput: Record<string, unknown> | null | undefined,
): FileEditDiff | null {
  return acpFileEditsFromToolCallRawInput(kind, rawInput)[0] ?? null
}
