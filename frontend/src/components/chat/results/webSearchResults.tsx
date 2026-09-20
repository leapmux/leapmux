import type { JSX } from 'solid-js'
import type { WebSearchLink, WebSearchRequest, WebSearchResult } from '../model/tools/webSearch'
import type { ToolResultRenderContext } from '../renderContext'
import { For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { pluralize } from '~/lib/plural'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { extractDomain } from '~/lib/url'
import { clippedText } from '~/styles/shared.css'
import { getToolResultExpanded, renderMarkdownForContext } from '../messageRenderers'
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
import { renderQueryTitle } from './tools/titleParts'
import { useCollapsedItems } from './useCollapsedLines'

/**
 * The queries a search ran BEYOND the first.
 *
 * The request states the first one, so listing it again in the body would repeat it.
 */
export function extraQueries(request: Pick<WebSearchRequest, 'queries'> | undefined): string[] {
  return request?.queries?.slice(1) ?? []
}

export function WebSearchResultsBody(props: {
  source: WebSearchResult
  /**
   * What the call asked for. The row's title states the action itself; this body
   * reads it for the QUERIES the title has no room for.
   */
  request?: WebSearchRequest
  context?: ToolResultRenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const links = () => props.source.links
  const { isCollapsed, displayItems: displayLinks } = useCollapsedItems<WebSearchLink>({ items: links, expanded })
  const queries = () => extraQueries(props.request)

  return (
    <div class={toolMessage}>
      {/* One search often runs several queries, and the row's TITLE states the
          first. The rest are what the expand control is for on a row that returned
          no links of its own. */}
      <Show when={expanded()}>
        <For each={queries()}>
          {query => <div class={toolInputSummary}>{renderQueryTitle(query) || query}</div>}
        </For>
      </Show>
      <Show when={links().length > 0}>
        <div class={toolResultPrompt}>
          {pluralize(links().length, 'result')}
        </div>
        <div class={`${webSearchLinkList}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
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
      <Show when={expanded() && props.source.summary}>
        <div class={toolResultContent} ref={cachedInnerHtml(() => renderMarkdownForContext(props.source.summary, props.context))} />
      </Show>
    </div>
  )
}
