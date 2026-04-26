import { isObject } from '~/lib/jsonPick'

export interface WebSearchLink {
  title: string
  url: string
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
