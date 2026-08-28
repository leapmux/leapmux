/**
 * One page of a keyset-paginated listing, as `fetchPage` reports it.
 *
 * `nextCursor` is the empty string after the final page.
 */
export interface ListPage<T> {
  items: T[]
  nextCursor: string
}

/**
 * Read EVERY page of a keyset-paginated listing, not the first.
 *
 * Every settings panel that lists a security-relevant collection shares this
 * loop, because each of them learned the same lesson separately: a single
 * call truncated the list silently, and a row the panel never drew was one
 * whose controls (Retire, Revoke, Disconnect) did not exist -- on the one
 * screen a person reaches for when they believe something is stolen.
 *
 * The loop does NOT trust the cursor alone to end it. A client loop whose
 * only exit is a value the server chooses is a hang the server can cause, so
 * it also stops when the cursor fails to advance and when it read `maxPages`
 * pages. Both limits sit far above any real account; `maxPages` is a runaway
 * guard, not a limit anybody reaches.
 *
 * Rows are keyed while assembling: a stalled cursor can make the last page
 * arrive twice, and a keyset boundary can repeat a row across two pages.
 * Neither should render the same row twice with two buttons on it. A repeated
 * key keeps the LAST occurrence, matching a server that re-reads the row.
 */
export async function fetchAllPages<T>(
  fetchPage: (cursor: string) => Promise<ListPage<T>>,
  opts: { maxPages: number, keyOf: (item: T) => string },
): Promise<T[]> {
  const byKey = new Map<string, T>()
  let cursor = ''
  for (let page = 0; page < opts.maxPages; page++) {
    const resp = await fetchPage(cursor)
    for (const item of resp.items)
      byKey.set(opts.keyOf(item), item)
    const next = resp.nextCursor
    if (next === '' || next === cursor)
      break
    cursor = next
  }
  return [...byKey.values()]
}

/**
 * The hub's maximum page, asked for explicitly: an omitted limit resolves to
 * the hub's default of fifty, and the loop above would then take ten times
 * the round trips and cover a tenth of what `maxPages` claims.
 */
export const PAGE_SIZE = 500

/**
 * Pages every listing reads before it stops. At PAGE_SIZE this covers a
 * quarter of a million rows, which is to say it is a runaway guard shared by
 * every panel, not a limit anybody reaches.
 */
export const MAX_PAGES = 500
