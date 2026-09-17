import { isObject } from '~/lib/jsonPick'

/** One web search: the first query, the further queries, or a find-in-page. */
export interface WebSearchRequest {
  query: string
  queries?: string[]
  inPage?: { pattern: string, url?: string }
}

/** One link a web search returned. */
export interface WebSearchLink {
  title: string
  url: string
}

/**
 * What the search found: the links, and the summary the agent wrote over them.
 *
 * The query is NOT here: the request states what the call looked for, and a second
 * copy beside the links could state a different one.
 */
export interface WebSearchResult {
  links: WebSearchLink[]
  summary: string
  /** Claude tool_use_result.durationSeconds (note: seconds, not ms). */
  durationSeconds?: number
}

/** Extract deduplicated links from WebSearch tool_use_result.results. */
export function extractWebSearchLinks(results: unknown[]): WebSearchLink[] {
  const seen = new Set<string>()
  const links: WebSearchLink[] = []
  for (const item of results) {
    if (isObject(item) && Array.isArray((item as Record<string, unknown>).content)) {
      for (const link of (item as Record<string, unknown>).content as Array<Record<string, unknown>>) {
        if (isObject(link) && typeof link.url === 'string' && typeof link.title === 'string' && !seen.has(link.url)) {
          seen.add(link.url)
          links.push({ title: link.title, url: link.url })
        }
      }
    }
  }
  return links
}

/** Extract the final text summary from WebSearch results (last string entry). */
export function extractWebSearchSummary(results: unknown[]): string {
  for (let i = results.length - 1; i >= 0; i--) {
    if (typeof results[i] === 'string' && (results[i] as string).trim().length > 0)
      return (results[i] as string).trim()
  }
  return ''
}
