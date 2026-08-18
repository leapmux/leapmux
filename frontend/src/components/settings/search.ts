/** One searchable setting, whichever of the three sources it came from. */
export interface SearchEntry {
  /** The navigation group's human title, for the breadcrumb. */
  groupTitle: string
  /** The navigation id a hit jumps to, and the key results group by. */
  navId: string
  label: string
  help?: string
  keywords?: string[]
  /** Enum option labels — searching "dark" should find the Theme row. */
  optionLabels?: string[]
}

/**
 * One entry plus its match text, folded to lower case once.
 *
 * The index is built from the descriptors, which do not change while the user
 * types; the match runs on every keystroke. Folding the case at index time
 * keeps the per-keystroke work to substring tests.
 */
export interface IndexedSearchEntry {
  entry: SearchEntry
  haystack: string
}

/** One navigation group's hits, in the order the navigation lists groups. */
export interface SearchGroup {
  navId: string
  groupTitle: string
  entries: SearchEntry[]
}

/** Fold one entry's searchable text into a single lower-case string. */
export function indexSearchEntry(entry: SearchEntry): IndexedSearchEntry {
  const parts = [
    entry.label,
    entry.help ?? '',
    entry.groupTitle,
    ...(entry.keywords ?? []),
    ...(entry.optionLabels ?? []),
  ]
  return { entry, haystack: parts.join(' ').toLowerCase() }
}

/** Build the match index for a whole entry list. */
export function buildSearchIndex(entries: readonly SearchEntry[]): IndexedSearchEntry[] {
  return entries.map(indexSearchEntry)
}

/**
 * Case-insensitive substring match over label, help, the group title,
 * keywords, and enum option labels. Returns the hits grouped by NAVIGATION
 * GROUP in `navOrder` order; groups with no hits are omitted. An empty or
 * whitespace-only query matches nothing (the dialog shows the normal panels).
 *
 * Grouping is by `navId` and not by category: a user group and an admin group
 * can share one category (both "Advanced"), and grouping by category then
 * rendered that category's hits once per group that claims it.
 */
export function matchSettings(
  index: readonly IndexedSearchEntry[],
  query: string,
  navOrder: readonly string[],
): SearchGroup[] {
  const q = query.trim().toLowerCase()
  if (q === '')
    return []
  const byNav = new Map<string, SearchEntry[]>()
  for (const { entry, haystack } of index) {
    if (!haystack.includes(q))
      continue
    let hits = byNav.get(entry.navId)
    if (!hits) {
      hits = []
      byNav.set(entry.navId, hits)
    }
    hits.push(entry)
  }
  const groups: SearchGroup[] = []
  for (const navId of navOrder) {
    const hits = byNav.get(navId)
    if (hits && hits.length > 0)
      groups.push({ navId, groupTitle: hits[0].groupTitle, entries: hits })
  }
  return groups
}

/** The breadcrumb label a result renders: `Group › Label`. */
export function breadcrumb(entry: SearchEntry): string {
  return `${entry.groupTitle} › ${entry.label}`
}
