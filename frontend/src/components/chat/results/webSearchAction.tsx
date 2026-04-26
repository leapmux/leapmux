import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { toolInputCode, toolInputText } from '../toolStyles.css'
import { renderQueryTitle, renderUrlTitle } from '../toolTitleRenderers'

export type WebSearchAction
  = | { type: 'search', query: string, queries: string[] }
    | { type: 'openPage', url: string }
    | { type: 'findInPage', pattern: string, url?: string }
    | { type: 'other', query: string }

export interface WebSearchActionSource {
  action: WebSearchAction
}

/**
 * Returns the textual detail of an action for fallback display and emptiness
 * checks. Caller-side dispatch (e.g. "skip render when detail is empty")
 * uses this without touching the body.
 */
export function webSearchActionDetail(action: WebSearchAction): string {
  if (action.type === 'search')
    return action.query
  if (action.type === 'openPage')
    return action.url
  if (action.type === 'findInPage') {
    if (action.pattern && action.url)
      return `'${action.pattern}' in ${action.url}`
    if (action.pattern)
      return `'${action.pattern}'`
    if (action.url)
      return action.url
    return ''
  }
  return action.query
}

/**
 * Per-action title fragment for the Codex `webSearch` action card (search /
 * openPage / findInPage / other). Wraps URLs and queries via the shared
 * tool-detail renderers so the formatting matches the rest of the chat UI.
 */
export function WebSearchActionBody(props: {
  source: WebSearchActionSource
  context?: RenderContext
}): JSX.Element {
  const action = () => props.source.action
  return <>{renderActionTitle(action())}</>
}

function renderActionTitle(action: WebSearchAction): JSX.Element | string {
  if (action.type === 'openPage')
    return renderUrlTitle(action.url) || action.url || 'Open page'
  if (action.type === 'search')
    return renderQueryTitle(action.query) || action.query || 'Web search'
  if (action.type === 'findInPage') {
    const url = action.url || ''
    const pattern = action.pattern
    if (pattern && url) {
      return (
        <>
          <span class={toolInputCode}>{`"${pattern}"`}</span>
          <span class={toolInputText}>{' in '}</span>
          {renderUrlTitle(url) || <span class={toolInputText}>{url}</span>}
        </>
      )
    }
    if (pattern)
      return <span class={toolInputCode}>{`"${pattern}"`}</span>
    if (url)
      return renderUrlTitle(url) || url
    return 'Find in page'
  }
  return action.query || 'Searching the web'
}
