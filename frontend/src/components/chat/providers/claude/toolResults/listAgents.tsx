import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Users from 'lucide-solid/icons/users'
import { getToolResultExpanded } from '../../../messageRenderers'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedFlag } from '../../../results/useCollapsedLines'

/**
 * The ListAgents listing, rendered as markdown.
 *
 * The CLI hands the whole listing over as ONE pre-formatted string rather than
 * as rows: its `mapToolResultToToolResultBlockParam` puts `data.listing`
 * straight into the tool_result content. Rendering that as markdown shows the
 * table it is today and keeps working if the CLI reformats it, which building
 * bespoke row markup around a shape we do not control would not.
 */
export function ListAgentsResultView(props: {
  listing: string
  context?: RenderContext
}): JSX.Element {
  const isCollapsed = useCollapsedFlag({
    text: () => props.listing,
    expanded: () => getToolResultExpanded(props.context),
  })

  return (
    <ToolStatusHeader icon={Users} title="Reachable agents">
      <CollapsibleContent
        kind="markdown-tool-result"
        text={props.listing}
        isCollapsed={isCollapsed()}
        context={props.context}
      />
    </ToolStatusHeader>
  )
}
