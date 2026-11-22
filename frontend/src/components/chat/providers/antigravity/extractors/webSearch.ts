import type { WebSearchLink, WebSearchRequest, WebSearchResult } from '../../../model/tools/webSearch'
import type { AntigravityStep } from './toolCommon'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Extracts the web search query from tool arguments or the native `searchWeb` payload. */
export function antigravityWebSearchRequest(step: AntigravityStep): WebSearchRequest {
  const query = pickString(step.args, 'query') || pickString(pickObject(step.raw, 'searchWeb'), 'query')
  return { query }
}

/** Extracts deduplicated links and the trimmed summary from an Antigravity `searchWeb` step. */
export function antigravityWebSearchResult(step: AntigravityStep): WebSearchResult {
  const search = pickObject(step.raw, 'searchWeb')
  const links: WebSearchLink[] = []
  const seenUrls = new Set<string>()
  const documents = search?.webDocuments
  if (Array.isArray(documents)) {
    for (const entry of documents) {
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
  }
  const summary = pickString(search, 'summary').trim()
  return { links, summary }
}
