import type { FileListEntry } from '../searchResult'
import { COLLAPSED_RESULT_ROWS } from '../collapse'

/** The directory a call asked to list. */
export interface ListRequest { path: string }

/** The entries the listing returned, drawn by `results/listResult.tsx`. */
export interface ListResult {
  entries: FileListEntry[]
  totalEntries?: number
  offset?: number
  truncated?: boolean
  notice?: string
}

/** Whether the listing holds more entries than the collapsed row shows. */
export function listResultCollapsible(result: ListResult): boolean {
  return result.entries.length > COLLAPSED_RESULT_ROWS
}
