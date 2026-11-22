import type { WebSearchRequest, WebSearchResult } from '../../../model/tools/webSearch'
import type { AntigravityStep } from './toolCommon'
import { pickObject, pickString } from '~/lib/jsonPick'
import { extractWebSearchLinks } from '../../webSearchLinks'

/** Extracts the web search query from tool arguments or the native `searchWeb` payload. */
export function antigravityWebSearchRequest(step: AntigravityStep): WebSearchRequest {
  const query = pickString(step.args, 'query').trim() || pickString(pickObject(step.raw, 'searchWeb'), 'query').trim()
  return { query }
}

/** Extracts deduplicated links and the trimmed summary from an Antigravity `searchWeb` step. */
export function antigravityWebSearchResult(step: AntigravityStep): WebSearchResult {
  const search = pickObject(step.raw, 'searchWeb')
  return {
    links: extractWebSearchLinks(search?.webDocuments),
    summary: pickString(search, 'summary').trim(),
  }
}
