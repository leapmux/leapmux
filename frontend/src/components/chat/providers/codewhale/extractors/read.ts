import type { ReadFileResult } from '../../../model/readFileResult'
import type { ReadRequest } from '../../../model/tools/read'
import { isObject, pickFirstString, pickNumber, pickString } from '~/lib/jsonPick'
import { readFileResultFromContent } from '../../../model/readFileResult'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'

/**
 * The paging notice the runtime appends to a partial read.
 *
 * `read` ends a window it cut with one bracketed line after a blank one --
 * `[Showing lines 1-40 of 90 (...). Use offset=41 to continue.]` or
 * `[12 more lines in file (...). Use offset=41 to continue.]` -- and the file itself
 * never holds it. It is split off so the body states file lines alone and the notice
 * reads as the notice it is.
 */
const PAGING_NOTICE = /\n\n(\[(?:Showing lines \d+-\d+ of \d+|\d+ more lines in file) [^\n]*\])$/

/**
 * The thing a read call addresses, as the row's header states it.
 *
 * A file tool states a path. `handle_read` states a handle -- an object or a compact
 * `session_id/name` string -- and `retrieve_tool_result` states the id of a stored
 * result, so each reads as the reference it is.
 */
export function codewhaleReadRequest(args: Record<string, unknown>): ReadRequest {
  const offset = pickNumber(args, 'offset')
  const limit = pickNumber(args, 'limit')
  return {
    path: pickFirstString(args, TOOL_FILE_PATH_KEYS) || codewhaleHandleText(args.handle) || pickString(args, 'ref') || pickString(args, 'id'),
    ...(offset !== null ? { offset } : {}),
    ...(limit !== null ? { limit } : {}),
  }
}

/** A `var_handle` object, or its compact string form, as one `session_id/name` string. */
function codewhaleHandleText(handle: unknown): string {
  if (typeof handle === 'string')
    return handle
  if (!isObject(handle))
    return ''
  const session = pickString(handle, 'session_id')
  const name = pickString(handle, 'name')
  return session && name ? `${session}/${name}` : name
}

/**
 * The file one read returned, numbered from the offset the call asked for.
 *
 * The runtime answers with the file's own lines and no numbers, so the body numbers
 * them from `offset` (1-indexed, the tool's own default). The paging notice becomes a
 * trailing reminder.
 */
export function codewhaleReadResult(text: string, request: ReadRequest): ReadFileResult {
  const notice = PAGING_NOTICE.exec(text)
  const content = notice ? text.slice(0, notice.index) : text
  const startLine = request.offset !== undefined && Number.isSafeInteger(request.offset) && request.offset > 0 ? request.offset : 1
  const result = readFileResultFromContent({ content, startLine })
  return notice?.[1] ? { ...result, trailing: [{ label: 'Notice', text: notice[1] }] } : result
}
