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
