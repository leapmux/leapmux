import type { MessageInitShape } from '@bufbuild/protobuf'
import type { PageRequestSchema, PageResponse } from '~/generated/proto/leapmux/v1/common_pb'
import { MAX_PAGE_LIMIT } from '~/generated/contracts/pagination'

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
 * Pages one listing reads before it stops. At `MAX_PAGE_LIMIT` (the hub's
 * maximum page, from contracts/pagination.json) this covers a quarter of a
 * million rows, which is to say it is a runaway guard shared by every listing,
 * not a limit anybody reaches.
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
 * The loop sends `{ limit: MAX_PAGE_LIMIT, cursor }`: it asks for the hub's
 * maximum page explicitly, because an omitted limit resolves to the hub's
 * default page, and the loop would then take ten times the round trips and
 * cover a tenth of what `MAX_PAGES` claims. It does NOT trust the cursor
 * alone to end it. A client loop whose only exit is a value the server chooses
 * is a hang the server can cause, so it also stops when the cursor fails to
 * advance and after `maxPages` pages (`MAX_PAGES` by default).
 *
 * Either stop REJECTS, as a failed page read does, and returns no rows. The
 * hub ends every listing with an empty cursor, so a stop means a fault in the
 * hub, and the rows read so far are not the listing. Resolving with them
 * repeated the silent truncation this loop exists to end. The CLI's
 * `readAllPages` (`backend/internal/cli/control/cmd/pages.go`) keeps the same
 * rule.
 *
 * Rows are keyed while assembling: a keyset boundary can repeat a row across
 * two pages, and the same row must not render twice with two buttons on it. A
 * repeated key keeps the position of its first occurrence and the value of its
 * last, matching a server that re-reads the row.
 *
 * `maxPages` must be a positive whole number. Any other value rejects with a
 * `RangeError` before the first read, because a limit that admits no page
 * cannot read the listing, and an empty result would read as an empty listing.
 */
export async function fetchAllPages<T>(
  fetchPage: (page: PageRequestInit) => Promise<ListPage<T>>,
  opts: { keyOf: (item: T) => string, maxPages?: number },
): Promise<T[]> {
  const maxPages = opts.maxPages ?? MAX_PAGES
  if (!Number.isSafeInteger(maxPages) || maxPages < 1)
    throw new RangeError(`fetchAllPages needs a positive whole number of pages, not ${maxPages}.`)
  const byKey = new Map<string, T>()
  let cursor = ''
  for (let read = 0; read < maxPages; read++) {
    const resp = await fetchPage({ limit: MAX_PAGE_LIMIT, cursor })
    for (const item of resp.items)
      byKey.set(opts.keyOf(item), item)
    const next = resp.page?.nextCursor ?? ''
    if (next === '') {
      return [
        ...byKey.values(),
      ]
    }
    if (next === cursor)
      throw new Error('The hub did not end the listing: the next-page cursor did not advance.')
    cursor = next
  }
  throw new Error(`The hub did not end the listing: it continued past ${maxPages} pages.`)
}
