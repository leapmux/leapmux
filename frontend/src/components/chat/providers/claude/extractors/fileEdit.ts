import type { StructuredPatchHunk } from '../../../diff'
import type { FileEditDiffSource } from '../../../results/fileEditDiff'
import { pickString } from '~/lib/jsonPick'
import { CLAUDE_TOOL } from '~/types/toolMessages'

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
 * Future work: Claude also sends `userModified` (true when the user
 * hand-edited the patch via the permission prompt) and `gitDiff` (rich git
 * status info). Surface these when there is a UI surface ready to use them.
 */
export function claudeFileEditFromToolUseResult(
  toolUseResult: Record<string, unknown> | null | undefined,
): FileEditDiffSource | null {
  if (!toolUseResult)
    return null
  const structuredPatch = Array.isArray(toolUseResult.structuredPatch)
    ? toolUseResult.structuredPatch as StructuredPatchHunk[]
    : null
  const filePath = pickString(toolUseResult, 'filePath')
  const oldStr = pickString(toolUseResult, 'oldString')
  const newStr = pickString(toolUseResult, 'newString')
  const originalFile = pickString(toolUseResult, 'originalFile', undefined)
  if (!structuredPatch && !filePath && oldStr === '' && newStr === '')
    return null
  return { filePath, structuredPatch, oldStr, newStr, originalFile }
}
