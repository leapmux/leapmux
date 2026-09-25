import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLINE_EVENT } from '~/generated/contracts/cline-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { CLINE_FIELD, clinePayload } from '../protocol'

/**
 * The two rows of one Cline tool call.
 *
 *   tool.started   {toolCallId, toolName, input}
 *   tool.finished  {toolCallId, toolName, output, error?}
 *
 * `output` is the tool's own result: a list of `{query, result, error?, success}`
 * records for the tools that run several operations (`read_files`,
 * `search_codebase`, `run_commands`, `fetch_web_content`), one such record for
 * `editor` and `apply_patch`, a string for a question and a skill, and
 * `{text, finishReason, ...}` for a subagent. A call that failed or that the reader
 * refused states `error` as text, and its `output` is `{error}`. A transcript that the
 * worker wrote from Cline's store states the stored result instead, which Cline keeps
 * as the same value or as its JSON text.
 *
 * `editor` and `apply_patch` catch their own failure: they answer with their one
 * record, with `success: false` and the reason in its `error`. Cline states an `error`
 * for the call only when the tool throws, so that record is where their failure is.
 *
 * A call that the turn outlived closes with its OWN `tool.started` row again, and the
 * completion is what tells that copy from the request.
 */

/** One `tool.started` row: the call's id, its tool and its arguments. */
export interface ClineToolStart {
  id: string
  name: string
  input: Record<string, unknown>
}

/** One `tool.finished` row: the call it ends, its result, and its error. */
export interface ClineToolFinish {
  id: string
  name: string
  output: unknown
  /**
   * The error Cline states for the call, else the error of the one operation record
   * that is the call's whole result, or '' for a call that succeeded.
   */
  error: string
}

/** The call one row starts, or null for a row that starts none. */
export function clineToolStart(payload: unknown): ClineToolStart | null {
  const data = clinePayload(payload, CLINE_EVENT.ToolStarted)
  if (!data)
    return null
  const id = pickString(data, CLINE_FIELD.ToolCallId)
  const name = pickString(data, CLINE_FIELD.ToolName)
  if (!id || !name)
    return null
  const input = data[CLINE_FIELD.Input]
  return { id, name, input: isObject(input) ? input : {} }
}

/** The call one row ends, or null for a row that ends none. */
export function clineToolFinish(payload: unknown): ClineToolFinish | null {
  const data = clinePayload(payload, CLINE_EVENT.ToolFinished)
  if (!data)
    return null
  const id = pickString(data, CLINE_FIELD.ToolCallId)
  if (!id)
    return null
  const output = data[CLINE_FIELD.Output]
  return {
    id,
    name: pickString(data, CLINE_FIELD.ToolName),
    output,
    error: errorText(data[CLINE_FIELD.Error]) || singleOperationError(output),
  }
}

/** The words of a failed operation record that states none. */
const OPERATION_FAILED = 'The operation failed.'

/**
 * The error of a result that is ONE operation record with `success: false`, or ''.
 *
 * Only a single record is the whole call. A list holds one record for each operation
 * that the call ran, and one failed operation is not a failure of the call: its error
 * stays in its own record, where the row draws it.
 */
function singleOperationError(output: unknown): string {
  const value = typeof output === 'string' ? parseJSON(output) : output
  if (!isObject(value) || !('query' in value) || value.success !== false)
    return ''
  return pickString(value, 'error').trim() || OPERATION_FAILED
}

/**
 * The words of a call's error. Cline states a refusal's error as the JSON text of
 * `{error}`, and the words inside it are the reason.
 */
export function errorText(value: unknown): string {
  if (typeof value !== 'string')
    return ''
  const text = value.trim()
  if (!text.startsWith('{'))
    return text
  try {
    const parsed: unknown = JSON.parse(text)
    return isObject(parsed) && typeof parsed.error === 'string' ? parsed.error : text
  }
  catch {
    return text
  }
}

/** The call id that one span side states, whichever side it is. */
export function clineSideCallId(side: ParsedMessageContent | undefined): string {
  if (!side)
    return ''
  return clineToolStart(side.parentObject)?.id ?? clineToolFinish(side.parentObject)?.id ?? ''
}

/** One `{query, result, error?, success}` record of an operation a tool ran. */
export interface ClineOperation {
  query: string
  result: string
  error: string
  success: boolean
}

/**
 * The operation records of a result, whichever of its shapes it takes: a list, one
 * record, or the JSON text of either. A value that is none of them yields none.
 */
export function clineOperations(output: unknown): ClineOperation[] {
  const value = typeof output === 'string' ? parseJSON(output) : output
  const records = Array.isArray(value) ? value : isObject(value) && 'query' in value ? [value] : []
  return records.filter(isObject).map(record => ({
    query: pickString(record, 'query'),
    result: pickString(record, 'result'),
    error: pickString(record, 'error'),
    success: record.success !== false,
  }))
}

/** The text of a result that is a string, or the JSON of one that is not. */
export function outputText(output: unknown): string {
  if (typeof output === 'string')
    return output
  if (output === undefined || output === null)
    return ''
  try {
    return JSON.stringify(output, null, 2)
  }
  catch {
    return String(output)
  }
}

/** The value of a JSON text, or undefined for text that is not JSON. */
export function parseJSON(text: string): unknown {
  try {
    return JSON.parse(text)
  }
  catch {
    return undefined
  }
}
