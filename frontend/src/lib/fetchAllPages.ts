import type { MessageInitShape } from '@bufbuild/protobuf'
import type { PageRequestSchema, PageResponse } from '~/generated/proto/leapmux/v1/common_pb'

/** The page that `fetchAllPages` asks `fetchPage` to read. */
export type PageRequestInit = MessageInitShape<typeof PageRequestSchema>

/**
 * One page of a keyset-paginated listing, as `fetchPage` reports it.
 *
 * `page` is the response's `PageResponse`. An empty `nextCursor` ends the
 * listing, and so does an absent `page`.
 */
export interface ListPage<T> {
  items: T[]
  page?: PageResponse | undefined
}

/**
 * The hub's maximum page, asked for explicitly: an omitted limit resolves to
 * the hub's default of fifty, and the loop below would then take ten times
 * the round trips and cover a tenth of what `MAX_PAGES` claims.
 */
export const PAGE_SIZE = 500

/**
 * Pages one listing reads before it stops. At PAGE_SIZE this covers a quarter
 * of a million rows, which is to say it is a runaway guard shared by every
 * listing, not a limit anybody reaches.
 */
export const MAX_PAGES = 500

/**
 * Read EVERY page of a keyset-paginated hub listing, not the first.
 *
 * Every surface that lists a collection with no upper limit shares this loop,
 * because each of them learned the same lesson separately: a single call
 * truncated the list silently. A settings row the panel never drew was one
 * whose controls (Retire, Revoke, Disconnect) did not exist, and a worker the
 * sidebar never drew was one the user could not open a tab on.
 *
 * The loop sends `{ limit: PAGE_SIZE, cursor }` and does NOT trust the cursor
 * alone to end it. A client loop whose only exit is a value the server chooses
 * is a hang the server can cause, so it also stops when the cursor fails to
 * advance and when it read `maxPages` pages (`MAX_PAGES` by default).
 *
 * Rows are keyed while assembling: a stalled cursor can make the last page
 * arrive twice, and a keyset boundary can repeat a row across two pages.
 * Neither should render the same row twice with two buttons on it. A repeated
 * key keeps the LAST occurrence, matching a server that re-reads the row.
 */
export async function fetchAllPages<T>(
  fetchPage: (page: PageRequestInit) => Promise<ListPage<T>>,
  opts: { keyOf: (item: T) => string, maxPages?: number },
): Promise<T[]> {
  const maxPages = opts.maxPages ?? MAX_PAGES
  const byKey = new Map<string, T>()
  let cursor = ''
  for (let read = 0; read < maxPages; read++) {
    const resp = await fetchPage({ limit: PAGE_SIZE, cursor })
    for (const item of resp.items)
      byKey.set(opts.keyOf(item), item)
    const next = resp.page?.nextCursor ?? ''
    if (next === '' || next === cursor)
      break
    cursor = next
  }
  return [
    ...byKey.values(),
  ]
}
