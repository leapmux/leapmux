import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { WebSearchLink } from './webSearchExtract'
import { For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { pluralize } from '~/lib/plural'
import { extractDomain } from '~/lib/url'
import { clippedText } from '~/styles/shared.css'
import { getToolResultExpanded, renderMarkdownForContext } from '../messageRenderers'
import {
  toolMessage,
  toolMetaRow,
  toolResultCollapsed,
  toolResultContent,
  toolResultPrompt,
  webSearchLinkDomain,
  webSearchLinkList,
} from '../toolStyles.css'
import { useCollapsedItems } from './useCollapsedLines'

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
  const expanded = () => getToolResultExpanded(props.context)
  const links = () => props.source.links
  const { isCollapsed, displayItems: displayLinks } = useCollapsedItems<WebSearchLink>({ items: links, expanded })

  return (
    <div class={toolMessage}>
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
                    <a href={link.url} target="_blank" rel="noopener noreferrer nofollow">{link.title}</a>
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
