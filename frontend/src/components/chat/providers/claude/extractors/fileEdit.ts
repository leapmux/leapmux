import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { FileChangeResult } from '../../../model/tools/fileChange'
import type { ClaudeToolRow } from './toolCommon'
import { isObject, pickString } from '~/lib/jsonPick'
import { fileEditContent, fileEditDiffFromWholeFile, normalizeStructuredPatchHunks, pickFileEditDiff } from '../../../model/fileEditDiff'
import { unparsedResult } from '../../../model/toolCall'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeToolFailureResult } from './failure'

/**
 * Build a FileEditDiff from the input of a Claude `Write` or `Edit`
 * tool_use. Returns null for any other tool. Empty/missing fields fall back
 * to the empty string so callers don't have to defend against undefined.
 */
export function claudeFileEditFromToolUseInput(
  toolName: string,
  input: Record<string, unknown> | null | undefined,
): FileEditDiff | null {
  if (toolName === CLAUDE_TOOL_NAMES.EDIT) {
    return {
      filePath: pickString(input, 'file_path'),
      structuredPatch: null,
      oldStr: pickString(input, 'old_string'),
      newStr: pickString(input, 'new_string'),
    }
  }
  if (toolName === CLAUDE_TOOL_NAMES.WRITE) {
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
 * Build a FileEditDiff from a Claude `tool_use_result` payload (the
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
): FileEditDiff | null {
  if (!toolUseResult)
    return null
  const structuredPatch = normalizeStructuredPatchHunks(toolUseResult.structuredPatch)
  const filePath = pickString(toolUseResult, 'filePath')
  const oldStr = pickString(toolUseResult, 'oldString')
  const newStr = pickString(toolUseResult, 'newString')
  const originalFile = pickString(toolUseResult, 'originalFile', undefined)
  if (!structuredPatch && !filePath && oldStr === '' && newStr === '')
    return null
  // `originalFile` rides only when the record stated one, so the absent key stays
  // absent rather than present-and-undefined.
  return {
    filePath,
    ...(originalFile !== undefined ? { originalFile } : {}),
    ...fileEditContent(structuredPatch, oldStr, newStr),
  }
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
): FileEditDiff | null {
  if (isError || !toolUseResult || toolUseResult.type !== 'create')
    return null
  const content = pickString(toolUseResult, 'content')
  return content ? fileEditDiffFromWholeFile(pickString(toolUseResult, 'filePath'), content, 'add') : null
}

/**
 * The RESULT side an edit, write, multi-edit or notebook-edit call shares: the
 * change that LANDED.
 *
 * The structured patch the file received, with the arguments as the fallback. A
 * FAILED call landed nothing, so it states its error text alone.
 *
 * The result half alone, because the KIND belongs to the caller. `CLAUDE_TOOL_KINDS`
 * maps the four file tools onto `edit` and `write`, and `CLAUDE_TOOL_READERS` holds one
 * entry for each -- so each entry states its own kind and the checker pairs it with
 * that kind's request. A builder that answered both kinds read the tool name a second
 * time to choose between them, which put the same mapping in two places.
 */
export function claudeFileChangeResult(args: ClaudeToolRow, result: ClaudeToolRow | undefined): { result?: FileChangeResult | ToolFailureResult | UnparsedToolResult } {
  if (!result)
    return {}
  const failure = claudeToolFailureResult(result)
  if (failure) {
    // The REQUEST stays, and the caller keeps it. A failed call draws no diff --
    // `RequestedChangesBody` owns that rule for every provider now -- but the row's
    // title is composed from the request's change list, so emptying it left a failed
    // edit reading as the bare word "Edit" with no way to tell which file the call
    // was about.
    return { result: failure }
  }
  const diff = pickFileEditDiff(
    claudeFileEditFromToolUseResult(result.toolUseResult),
    claudeFileEditFromToolUseInput(args.toolName, args.input),
  ) ?? claudeCreateResultDiff(result.toolUseResult, false)
  return diff ? { result: { changes: [diff] } } : { result: unparsedResult(result.resultContent) }
}

/**
 * The changes the arguments state: one per substitution, one for a write.
 *
 * `CLAUDE_TOOL_REQUEST_OVERRIDES` reads this for both the edit kind and the write kind,
 * which is why it takes the tool NAME beside the arguments: four tools share those two
 * kinds and each states its change in its own keys.
 *
 * The paired RESULT answers the no-sibling case. A `tool_use_result` that arrives with
 * no `tool_use` states the whole change on its own side -- the structured patch, or the
 * created file's whole body -- and the arguments state nothing. The row still heads
 * itself with the file that changed, so the result's diff fills the request's list when
 * the arguments name no file. Without it the change list was empty, the file-change
 * invariant (I7) refused the draft, and the row degraded to a generic card that showed
 * neither the diff nor the file.
 */
export function claudeFileEditChanges(input: Record<string, unknown>, toolName: string, result: ClaudeToolRow | undefined): FileEditDiff[] {
  const fromInput = claudeFileEditChangesFromInput(input, toolName)
  if (fromInput.some(change => change.filePath !== ''))
    return fromInput
  const fromResult = result
    ? claudeFileEditFromToolUseResult(result.toolUseResult) ?? claudeCreateResultDiff(result.toolUseResult, false)
    : null
  return fromResult ? [fromResult] : fromInput
}

function claudeFileEditChangesFromInput(input: Record<string, unknown>, toolName: string): FileEditDiff[] {
  if (toolName === CLAUDE_TOOL_NAMES.MULTI_EDIT) {
    // One entry per substitution: no single pair describes a list of them.
    const edits = Array.isArray(input.edits) ? input.edits.filter(isObject) : []
    return edits.map((edit) => {
      const single = claudeFileEditFromToolUseInput(CLAUDE_TOOL_NAMES.EDIT, { file_path: pickString(input, 'file_path'), old_string: pickString(edit, 'old_string'), new_string: pickString(edit, 'new_string') })
      return single
    }).filter((source): source is FileEditDiff => source !== null)
  }
  if (toolName === CLAUDE_TOOL_NAMES.NOTEBOOK_EDIT) {
    // The notebook carries its file under `notebook_path`; the change reads like an edit.
    const path = pickString(input, 'notebook_path')
    return [{ filePath: path, structuredPatch: null, oldStr: pickString(input, 'old_source'), newStr: pickString(input, 'new_source') }]
  }
  const single = claudeFileEditFromToolUseInput(toolName, input)
  return single ? [single] : []
}
