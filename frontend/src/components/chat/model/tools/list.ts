import type { FileListEntry } from '../searchResult'

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
