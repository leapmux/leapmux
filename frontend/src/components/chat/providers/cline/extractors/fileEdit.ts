import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { EditRequest, EditResult } from '../../../model/tools/edit'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { fileEditDiffFromOldNew } from '../../../model/fileEditDiff'
import { CLINE_TOOL_NAME } from '../toolNames'
import { clineOperations } from './toolCommon'

/**
 * Cline's two file tools.
 *
 *   editor       {path, old_text?, new_text, insert_line?}  ->  {query, result, success}
 *   apply_patch  {input: "*** Begin Patch ..."}             ->  {query, result, success}
 *
 * `editor` replaces `old_text` with `new_text`, inserts `new_text` at `insert_line`, or
 * creates the file with `new_text` when it does not exist. `apply_patch` states a whole
 * patch in the format of OpenAI's patch tool. Both state the change in their arguments,
 * and the result confirms that it landed, in words.
 */

/** The changes one file call asked for. */
export function clineEditRequest(toolName: string, args: Record<string, unknown>): EditRequest {
  if (toolName === CLINE_TOOL_NAME.ApplyPatch)
    return { changes: applyPatchFileChanges(pickString(args, 'input')) ?? [] }
  const path = pickString(args, 'path')
  if (!path)
    return { changes: [] }
  const newText = pickString(args, 'new_text')
  // An insertion states no before side: the text lands between two lines.
  const insertLine = pickNumber(args, 'insert_line', undefined)
  const oldText = insertLine === undefined ? pickString(args, 'old_text') : ''
  const change: FileEditDiff = fileEditDiffFromOldNew(path, oldText, newText)
  return { changes: [change] }
}

/**
 * The changes that landed, or null when the result does not confirm them. Cline
 * confirms a change in words, and the change that landed is the one the call asked for.
 */
export function clineEditResult(request: EditRequest, output: unknown): EditResult | null {
  const operations = clineOperations(output)
  if (operations.length === 0 || operations.some(operation => !operation.success))
    return null
  return request.changes.length > 0 ? { changes: request.changes } : null
}
