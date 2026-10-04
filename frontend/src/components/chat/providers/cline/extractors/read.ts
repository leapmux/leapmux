import type { NumberedFileLine, ReadFileResult } from '../../../model/readFileResult'
import type { ReadRequest } from '../../../model/tools/read'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { clineOperations } from './toolCommon'

/**
 * Cline's `read_files` tool.
 *
 *   {files: [{path, start_line?, end_line?}]}  ->  [{query, result, error?, success}]
 *
 * One call reads several files, and its result holds one record for each. A record's
 * `result` numbers each line as `<n> | <text>`, with the number padded to the width of
 * the largest one.
 */

const NUMBERED_LINE = /^\s*(\d+) \| ?(.*)$/

/** The files one call states. The first file is the request's path; each other one follows it. */
export function clineReadRequest(args: Record<string, unknown>): ReadRequest {
  const files = Array.isArray(args.files) ? args.files.filter(isObject) : []
  const first = files[0]
  if (!first)
    return { path: pickString(args, 'path') }
  const path = files.map(file => pickString(file, 'path')).filter(Boolean).join(', ')
  if (files.length > 1)
    return { path }
  const start = pickNumber(first, 'start_line', undefined)
  const end = pickNumber(first, 'end_line', undefined)
  if (start === undefined || !Number.isInteger(start) || start < 1)
    return { path }
  const limit = end !== undefined && Number.isInteger(end) && end >= start ? end - start + 1 : undefined
  return { path, offset: start, ...(limit !== undefined ? { limit } : {}) }
}

/** The numbered lines of one file's text, or null when a line is not numbered. */
function numberedLines(content: string): NumberedFileLine[] | null {
  const lines: NumberedFileLine[] = []
  for (const line of content.replace(/\n$/, '').split('\n')) {
    const match = NUMBERED_LINE.exec(line)
    if (!match?.[1])
      return null
    lines.push({ num: Number(match[1]), text: match[2] ?? '' })
  }
  return lines
}

/**
 * What one call read, or null for a result with no record. A call that read one file
 * draws its numbered lines; a call that read several draws each file's record under its
 * query, as text.
 */
export function clineReadResult(output: unknown): ReadFileResult | null {
  const operations = clineOperations(output)
  const only = operations.length === 1 ? operations[0] : undefined
  if (only) {
    if (!only.success)
      return { lines: null, fallbackContent: only.error || only.result }
    if (only.result === '')
      return { lines: [], fallbackContent: '' }
    return { lines: numberedLines(only.result), fallbackContent: only.result }
  }
  if (operations.length === 0)
    return null
  const text = operations
    .map(operation => `${operation.query}\n${operation.success ? operation.result : operation.error}`)
    .join('\n\n')
  return { lines: null, fallbackContent: text }
}
