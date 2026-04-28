import Globe from 'lucide-solid/icons/globe'
import { createMemo, For, Show } from 'solid-js'
import { CODEX_ITEM } from '~/types/toolMessages'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { WebSearchActionBody, webSearchActionDetail } from '../../../results/webSearchAction'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderQueryTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexWebSearchActionFromItem } from '../extractors/webSearch'

// Registry-only: dispatched by `item.type === 'webSearch'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.WEB_SEARCH],
  render: (props) => {
    const action = createMemo(() => codexWebSearchActionFromItem(props.item))
    const queries = createMemo(() => {
      const a = action()
      return a?.type === 'search' ? a.queries : []
    })
    const detail = createMemo(() => {
      const a = action()
      return a ? webSearchActionDetail(a) : ''
    })
    const isStartMessage = (): boolean => {
      const a = action()
      return a?.type === 'other' && !a.query.trim()
    }
    const extraQueries = createMemo(() => queries().slice(1))
    const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.CODEX_WEB_SEARCH)

    return (
      <Show when={action()}>
        {a => (
          <Show
            when={!isStartMessage()}
            fallback={(
              <ToolUseLayout
                icon={Globe}
                toolName="WebSearch"
                title="Searching the web"
                alwaysVisible
                context={props.context}
              />
            )}
          >
            <Show when={detail().trim()}>
              <ToolUseLayout
                icon={Globe}
                toolName={a().type === 'openPage' ? 'WebFetch' : 'WebSearch'}
                title={<WebSearchActionBody source={{ action: a() }} context={props.context} />}
                context={props.context}
                expanded={expanded()}
                onToggleExpand={queries().length > 1 ? () => setExpanded(v => !v) : undefined}
                alwaysVisible={queries().length <= 1}
              >
                <Show when={extraQueries().length > 0}>
                  <For each={extraQueries()}>
                    {extraQuery => (
                      <div class={toolInputSummary}>
                        {renderQueryTitle(extraQuery) || extraQuery}
                      </div>
                    )}
                  </For>
                </Show>
              </ToolUseLayout>
            </Show>
          </Show>
        )}
      </Show>
    )
  },
})
