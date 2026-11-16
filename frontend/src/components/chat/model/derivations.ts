import type { ToolCallRow, ToolSpanRowPosition } from './row'

/**
 * Where one row sits in its span, on its own.
 *
 * A renderer view carries this WHOLE rather than restating `role` and the flag, so
 * the union's rule -- a row is never its own sibling -- reaches every reader instead
 * of stopping at the row.
 */
export function toolRowPosition(row: ToolCallRow): ToolSpanRowPosition {
  switch (row.role) {
    case 'request':
      return { role: row.role }
    case 'update':
      return { role: row.role, hasRequestRow: row.hasRequestRow }
    case 'result':
      return {
        role: row.role,
        hasRequestRow: row.hasRequestRow,
        ...(row.restatesRequest === true
          ? { restatesRequest: true }
          : {}),
      }
  }
}

/**
 * Whether the span's REQUEST is a row beside this one.
 *
 * Always false on the request itself, which the type states by leaving the flag out of
 * that branch. Every reader reads the flag through this one place, so no caller has to
 * know that an absent flag means no.
 */
export function rowHasRequestRow(row: ToolCallRow): boolean {
  return row.hasRequestRow ?? false
}

/**
 * Whether this row draws the result: only a result row does.
 *
 * A request or update row never draws a result, because it never reads one: the
 * result is a later message of the span, and a row reads only its own message and
 * the earlier request. While the call runs, the pending row that follows the request
 * row draws its preview and its live output instead.
 */
export function rowDrawsResult(row: ToolCallRow): boolean {
  return row.role === 'result'
}

/**
 * Whether this row draws the request: a request row, a result or update row with no
 * request row beside it, or a result row that restates a request row that states no
 * input (see ToolSpanRowPosition).
 */
export function rowDrawsRequest(row: ToolCallRow): boolean {
  return row.role === 'request'
    || !rowHasRequestRow(row)
    || (row.role === 'result' && row.restatesRequest === true)
}
