import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ZCodeResultDisplay } from '../extractors/display'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import ClockFading from 'lucide-solid/icons/clock-fading'
import OctagonX from 'lucide-solid/icons/octagon-x'
import { Show } from 'solid-js'
import { getToolResultExpanded } from '../../../messageRenderers'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { CommandInputBody } from '../../../results/multiLineCommandBody'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedLines } from '../../../results/useCollapsedLines'

const STATUS_ICON = { success: Check, failed: CircleAlert, waiting: ClockFading, stopped: OctagonX }

export function ZCodeDisplayBody(props: { source: ZCodeResultDisplay, hasRequest?: boolean, context?: RenderContext }): JSX.Element {
  const output = () => props.source.kind === 'mcp' ? '' : props.source.output
  const collapsed = useCollapsedLines({ text: output, expanded: () => getToolResultExpanded(props.context) })
  const text = () => (
    <Show when={output()}>
      <CollapsibleContent kind="ansi-or-pre" text={output()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} context={props.context} />
    </Show>
  )
  const body = (): JSX.Element => {
    const source = props.source
    if (source.kind === 'mcp') {
      return <McpToolMessage source={source.source} role="result" hasRequest={props.hasRequest} context={props.context} />
    }
    if (source.kind === 'status') {
      return (
        <ToolStatusHeader icon={STATUS_ICON[source.status]} title={source.title}>
          <Show when={source.command}>
            {command => <CommandInputBody command={command()} context={props.context} />}
          </Show>
          {text()}
        </ToolStatusHeader>
      )
    }
    return text()
  }
  return <>{body()}</>
}
