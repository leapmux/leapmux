import type { JSX } from 'solid-js'
import type { WebSearchLink, WebSearchRequest, WebSearchResult } from '../model/tools/webSearch'
import type { ToolResultRenderContext } from '../renderContext'
import { For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { pluralize } from '~/lib/plural'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { extractDomain } from '~/lib/url'
import { clippedText } from '~/styles/shared.css'
import { MarkdownPrefetch, MarkdownText } from '../MarkdownText'
import { getToolResultExpanded } from '../messageRenderers'
import {
  toolInputSummary,
  toolMessage,
  toolMetaRow,
  toolResultCollapsed,
  toolResultContent,
  toolResultPrompt,
  webSearchLinkDomain,
  webSearchLinkList,
} from '../toolStyles.css'
import { COLLAPSED_RESULT_ROWS } from './collapse'
import { renderQueryTitle } from './tools/titleParts'
import { useCollapsedItems } from './useCollapsedLines'

/**
 * The queries the header does not already show. Some calls use a separate
 * objective as the header and list all of their keywords in the body.
 */
export function extraQueries(request: Pick<WebSearchRequest, 'query' | 'queries'>): string[] {
  const queries = request.queries ?? []
  return queries[0] === request.query
    ? queries.slice(1)
    : queries
}

/**
 * The queries a search ran beyond the first, drawn on Expand.
 *
 * One search often runs several queries, and the row's TITLE states the first. The
 * queries are arguments of the call, so the row that draws the REQUEST states them: it
 * never waits for the result, and the result row draws only what the search found.
 */
export function WebSearchQueries(props: { request: WebSearchRequest, context?: ToolResultRenderContext }): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  return (
    <Show when={expanded() && extraQueries(props.request).length > 0}>
      <div class={toolMessage}>
        <For each={extraQueries(props.request)}>
          {query => <div class={toolInputSummary}>{renderQueryTitle(query) || query}</div>}
        </For>
      </div>
    </Show>
  )
}

/** Whether a web search result hides extra links or a summary when collapsed. */
export function webSearchResultCollapsible(source: WebSearchResult): boolean {
  return source.links.length > COLLAPSED_RESULT_ROWS || source.summary.length > 0
}

/** What a search found: its links and its summary. */
export function WebSearchResultsBody(props: {
  source: WebSearchResult
  context?: ToolResultRenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const links = () => props.source.links
  const { isCollapsed, displayItems: displayLinks } = useCollapsedItems<WebSearchLink>({ items: links, expanded })

  return (
    <div class={toolMessage}>
      <Show when={links().length > 0}>
        <div class={toolResultPrompt}>
          {pluralize(links().length, 'result')}
        </div>
        <div class={`${webSearchLinkList}${isCollapsed()
          ? ` ${toolResultCollapsed}`
          : ''}`}
        >
          <For each={displayLinks()}>
            {link => (
              <div class={toolMetaRow}>
                {/* The raw style plus a hand-built `Tooltip`, not `ClippedText`:
                    the label has to hold the <a>, and `ClippedText` renders a
                    plain string. The clip must stay on the SPAN, because an
                    inline non-replaced box like the <a> ignores `overflow` and
                    would lose the ellipsis. A search result title runs far past
                    this panel, so the tooltip is the only route to the rest. */}
                <Tooltip text={link.title} showWhen="clipped">
                  <span class={clippedText}>
                    {/* The title comes from a search result, so the text and
                        the address are two different strangers' words. */}
                    <a href={link.url} target="_blank" rel="noopener noreferrer nofollow" {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}>{link.title}</a>
                  </span>
                </Tooltip>
                <span class={webSearchLinkDomain}>{extractDomain(link.url)}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show
        when={expanded() && props.source.summary}
        fallback={<MarkdownPrefetch text={props.source.summary} context={props.context} />}
      >
        <MarkdownText
          text={props.source.summary}
          class={toolResultContent}
          context={props.context}
        />
      </Show>
    </div>
  )
}
