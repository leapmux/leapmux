import type { ToolCallRow, ToolSpanRowPosition } from './row'

/**
 * Where one row sits in its span, on its own.
 *
 * A renderer view carries this WHOLE rather than restating `role` and the two flags,
 * so the union's rule -- a row is never its own sibling -- reaches every reader
 * instead of stopping at the row.
 */
export function toolRowPosition(row: ToolCallRow): ToolSpanRowPosition {
  return row.role === 'request'
    ? { role: row.role, hasResultRow: row.hasResultRow }
    : row.role === 'result'
      ? { role: row.role, hasRequestRow: row.hasRequestRow }
      : { role: row.role, hasRequestRow: row.hasRequestRow, hasResultRow: row.hasResultRow }
}

/**
 * Whether the span's REQUEST is a row beside this one.
 *
 * Always false on the request itself, which the type states by leaving the flag out of
 * that branch. These two read the flag through one place, so no caller has to know
 * that an absent flag means no.
 */
export function rowHasRequestRow(row: ToolCallRow): boolean {
  return row.hasRequestRow ?? false
}

/** Whether the span's RESULT is a row beside this one. Always false on the result itself. */
export function rowHasResultRow(row: ToolCallRow): boolean {
  return row.hasResultRow ?? false
}

/** Whether this row draws the result: a result row, or a request/update row with no result row beside it. */
export function rowDrawsResult(row: ToolCallRow): boolean {
  return row.role === 'result' || !rowHasResultRow(row)
}

/** Whether this row draws the request: a request row, or a result/update with no visible request row. */
export function rowDrawsRequest(row: ToolCallRow): boolean {
  return row.role === 'request' || !rowHasRequestRow(row)
}
