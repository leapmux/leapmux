import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import { pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { fileEditDiffFromNewFile, normalizeStructuredPatchHunks } from '../../../results/fileEditDiff'

/** Tool names whose tool_use input describes a file edit. */
export function isClaudeFileEditTool(toolName: string): boolean {
  return toolName === CLAUDE_TOOL.EDIT || toolName === CLAUDE_TOOL.WRITE
}

/**
 * Build a FileEditDiffSource from the input of a Claude `Write` or `Edit`
 * tool_use. Returns null for any other tool. Empty/missing fields fall back
 * to the empty string so callers don't have to defend against undefined.
 */
export function claudeFileEditFromToolUseInput(
  toolName: string,
  input: Record<string, unknown> | null | undefined,
): FileEditDiffSource | null {
  if (toolName === CLAUDE_TOOL.EDIT) {
    return {
      filePath: pickString(input, 'file_path'),
      structuredPatch: null,
      oldStr: pickString(input, 'old_string'),
      newStr: pickString(input, 'new_string'),
    }
  }
  if (toolName === CLAUDE_TOOL.WRITE) {
    return {
      filePath: pickString(input, 'file_path'),
      structuredPatch: null,
      oldStr: '',
      newStr: pickString(input, 'content'),
    }
  }
  return null
}

/**
 * Build a FileEditDiffSource from a Claude `tool_use_result` payload (the
 * `tool_use_result` field that hangs off a user message wrapping a
 * `tool_result`). Returns null when the payload carries no edit-related
 * fields at all. Surfaces the structuredPatch when present so the picker can
 * prefer it over the tool_use-derived fallback.
 *
 * Claude may also send `userModified` (true when the user hand-edited the
 * patch via the permission prompt) and `gitDiff` (rich git status info).
 */
export function claudeFileEditFromToolUseResult(
  toolUseResult: Record<string, unknown> | null | undefined,
): FileEditDiffSource | null {
  if (!toolUseResult)
    return null
  const structuredPatch = normalizeStructuredPatchHunks(toolUseResult.structuredPatch)
  const filePath = pickString(toolUseResult, 'filePath')
  const oldStr = pickString(toolUseResult, 'oldString')
  const newStr = pickString(toolUseResult, 'newString')
  const originalFile = pickString(toolUseResult, 'originalFile', undefined)
  if (!structuredPatch && !filePath && oldStr === '' && newStr === '')
    return null
  return { filePath, structuredPatch, oldStr, newStr, originalFile }
}

/**
 * The diff a Claude `Write`/`create` tool_result renders when it carries the whole
 * new file in `content` (type 'create') with no structuredPatch/oldString/newString
 * AND no paired tool_use sibling to recover the input-side diff from: the new body
 * as an all-added diff. Null for a non-create, an error (the edit was not applied),
 * or a create with empty content. SHARED by renderer paths so they all recover the
 * same all-added diff instead of dropping to a one-line success.
 */
export function claudeCreateResultDiff(
  toolUseResult: Record<string, unknown> | null | undefined,
  isError: boolean,
): FileEditDiffSource | null {
  if (isError || !toolUseResult || toolUseResult.type !== 'create')
    return null
  const content = pickString(toolUseResult, 'content')
  return content ? fileEditDiffFromNewFile(pickString(toolUseResult, 'filePath'), content) : null
}
