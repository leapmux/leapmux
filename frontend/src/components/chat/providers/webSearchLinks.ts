import type { WebSearchLink } from '../model/tools/webSearch'
import { isObject, pickString } from '~/lib/jsonPick'

/**
 * Normalizes a provider's raw link list into deduplicated `WebSearchLink` records.
 *
 * Skips non-object items and entries with missing or blank URLs, trims URLs and
 * titles, deduplicates by trimmed URL in first-seen order, and falls back to the
 * URL when `title` is absent or blank.
 */
export function extractWebSearchLinks(entries: unknown): WebSearchLink[] {
  if (!Array.isArray(entries))
    return []
  const links: WebSearchLink[] = []
  const seenUrls = new Set<string>()
  for (const entry of entries) {
    if (!isObject(entry))
      continue
    const url = pickString(entry, 'url').trim()
    if (!url || seenUrls.has(url))
      continue
    seenUrls.add(url)
    links.push({
      title: pickString(entry, 'title').trim() || url,
      url,
    })
  }
  return links
}
