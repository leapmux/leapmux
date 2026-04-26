import type { WebSearchAction } from '../../../results/webSearchResult'
import { isObject, pickString } from '~/lib/jsonPick'

/**
 * Pull the action object from a Codex `webSearch` item. Returns null if the
 * item carries no recognizable action.
 */
export function codexWebSearchActionFromItem(
  item: Record<string, unknown> | null | undefined,
): WebSearchAction | null {
  if (!item || !isObject(item))
    return null
  const action = isObject(item.action) ? item.action as Record<string, unknown> : null
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
      url: url || undefined,
    }
  }

  if (actionType === 'search') {
    const direct = pickString(action, 'query').trim()
    const listed = Array.isArray(action?.queries)
      ? action.queries.filter(q => typeof q === 'string').map(q => (q as string).trim()).filter(Boolean)
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
