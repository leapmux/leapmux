import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * What the agent DID on the web, as Codex alone reports it.
 *
 * Codex sends one of these on a `webSearch` item; every other provider states the
 * query and the results alone. The row build reads it twice: once for the KIND
 * (`openPage` is a fetch, the rest are a web search), then for the typed request.
 */
export type WebSearchAction
  = | { type: 'search', query: string, queries: string[] }
    | { type: 'openPage', url: string }
    | { type: 'findInPage', pattern: string, url?: string }
    | { type: 'other', query: string }

/**
 * Pull the action object from a Codex `webSearch` item. Returns null if the
 * item carries no recognizable action.
 */
export function codexWebSearchActionFromItem(
  item: Record<string, unknown> | null | undefined,
): WebSearchAction | null {
  if (!item)
    return null
  const action = pickObject(item, 'action')
  const query = pickString(item, 'query')
  const actionType = pickString(action, 'type')

  if (actionType === 'openPage') {
    return { type: 'openPage', url: pickString(action, 'url', query) }
  }

  if (actionType === 'findInPage') {
    const url = pickString(action, 'url')
    return {
      type: 'findInPage',
      pattern: pickString(action, 'pattern'),
      ...(url ? { url } : {}),
    }
  }

  if (actionType === 'search') {
    const direct = pickString(action, 'query').trim()
    const listed = Array.isArray(action?.queries)
      ? action.queries.filter(q => typeof q === 'string').map(q => q.trim()).filter(Boolean)
      : []
    const merged = direct ? [direct, ...listed] : listed
    const queries = merged.filter((q, i) => merged.indexOf(q) === i)
    const top = queries[0] ?? query
    return { type: 'search', query: top, queries }
  }

  // 'other' action or no action: a placeholder "Searching the web" message
  // when there's no query, otherwise a plain query echo.
  return { type: 'other', query }
}
