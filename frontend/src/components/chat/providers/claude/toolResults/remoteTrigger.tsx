import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { RemoteTriggerResultSource } from '../extractors/remoteTrigger'
import Check from 'lucide-solid/icons/check'
import OctagonAlert from 'lucide-solid/icons/octagon-alert'
import { createMemo, Show } from 'solid-js'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickString } from '~/lib/jsonPick'
import { getToolResultExpanded } from '../../../messageRenderers'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedFlag } from '../../../results/useCollapsedLines'

/**
 * Header + collapsible JSON body for a Claude `RemoteTrigger` tool_result.
 * Header shows `HTTP {status}` plus a best-effort `name (id)` summary; body
 * is the pretty-printed JSON response.
 */
export function RemoteTriggerResultView(props: {
  source: RemoteTriggerResultSource
  context?: RenderContext
}): JSX.Element {
  const pretty = createMemo(() => prettifyJson(props.source.parsed ?? props.source.json))
  const isCollapsed = useCollapsedFlag({
    text: pretty,
    expanded: () => getToolResultExpanded(props.context),
  })
  const ok = () => props.source.status >= 200 && props.source.status < 300
  const icon = () => ok() ? Check : OctagonAlert

  const title = () => {
    const status = `HTTP ${props.source.status}`
    const trigger = props.source.trigger ?? undefined
    const name = pickString(trigger, 'name')
    const id = pickString(trigger, 'id')
    const tail = name && id ? `${name} (${id})` : (name || id)
    return tail ? `${status} · ${tail}` : status
  }

  return (
    <ToolStatusHeader icon={icon()} title={title()}>
      <Show when={pretty()}>
        <CollapsibleContent
          kind="json"
          text={pretty()}
          isCollapsed={isCollapsed()}
        />
      </Show>
    </ToolStatusHeader>
  )
}
