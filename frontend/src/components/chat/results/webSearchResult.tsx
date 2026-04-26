/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { For, Show } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { extractDomain } from '~/lib/url'
import { COLLAPSED_RESULT_ROWS } from '../toolRenderers'
import {
  toolInputCode,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultPrompt,
  webSearchLink,
  webSearchLinkDomain,
  webSearchLinkList,
  webSearchLinkTitle,
} from '../toolStyles.css'
import { renderQueryTitle, renderUrlTitle } from '../toolTitleRenderers'

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

// ---------------------------------------------------------------------------
// Claude: results-style WebSearch (links + summary)
// ---------------------------------------------------------------------------

export interface WebSearchResultsSource {
  links: WebSearchLink[]
  summary: string
  /** Echoed query (Claude tool_use_result.query). */
  query?: string
  /** Claude tool_use_result.durationSeconds (note: seconds, not ms). */
  durationSeconds?: number
}

export function WebSearchResultsBody(props: {
  source: WebSearchResultsSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const links = () => props.source.links
  const isCollapsed = () => !expanded() && links().length > COLLAPSED_RESULT_ROWS
  const displayLinks = () => {
    if (expanded() || links().length <= COLLAPSED_RESULT_ROWS)
      return links()
    return links().slice(0, COLLAPSED_RESULT_ROWS)
  }

  return (
    <div class={toolMessage}>
      <Show when={links().length > 0}>
        <div class={toolResultPrompt}>
          {pluralize(links().length, 'result')}
        </div>
        <div class={`${webSearchLinkList}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
          <For each={displayLinks()}>
            {link => (
              <div class={webSearchLink}>
                <span class={webSearchLinkTitle}>
                  <a href={link.url} target="_blank" rel="noopener noreferrer nofollow">{link.title}</a>
                </span>
                <span class={webSearchLinkDomain}>{extractDomain(link.url)}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show when={expanded() && props.source.summary}>
        <div class={toolResultContent} innerHTML={renderMarkdown(props.source.summary)} />
      </Show>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Codex: action-style WebSearch (intent, not results)
// ---------------------------------------------------------------------------

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
