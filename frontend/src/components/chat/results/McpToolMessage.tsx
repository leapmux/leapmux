import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { McpToolCallSource } from './mcpToolCall'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { messageCompletionFromProto } from '../assembledMessage'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { renderMcpTitle } from '../toolTitleRenderers'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { McpToolCallBody, mcpToolCallCollapsible, mcpToolCallDisplayName } from './mcpToolCall'
import { ToolHeaderRow } from './ToolStatusHeader'

/** Keep the request header separate from the argument and result body. */
export function McpToolMessage(props: {
  source: McpToolCallSource
  role: 'request' | 'result'
  hasRequest?: boolean
  failureLabel?: string
  context?: RenderContext
}): JSX.Element {
  const completion = () => messageCompletionFromProto(props.context?.sources?.current()?.completion)
  const source = createMemo(() => completion() === 'interrupted' || completion() === 'error'
    ? { ...props.source, status: 'failed' as const }
    : props.source)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const showHeader = () => props.role === 'request' || !props.hasRequest
  const args = createMemo(() => {
    if (!showHeader() || !props.source.argsJson)
      return undefined
    try {
      return JSON.parse(props.source.argsJson) as unknown
    }
    catch {
      return undefined
    }
  })
  const body = () => (
    <>
      <Show when={!props.context?.completionHeader && source().status === 'failed'}>
        <ToolHeaderRow icon={CircleAlert} title={completion() === 'interrupted' ? 'Interrupted' : props.failureLabel || 'Failed'} />
      </Show>
      <McpToolCallBody source={source()} context={props.context} expanded={expanded} />
    </>
  )
  return (
    <ToolMessageLayout
      role={props.role}
      hasRequest={props.hasRequest}
      icon={Wrench}
      toolName="MCP Tool Call"
      title={renderMcpTitle(mcpToolCallDisplayName(props.source), args())}
      context={props.context}
      showHeaderActions={!props.context?.hasOuterToolbar}
      expanded={expanded()}
      onToggleExpand={props.role === 'result' && mcpToolCallCollapsible(props.source) ? () => setExpanded(value => !value) : undefined}
      alwaysVisible
    >
      <Show when={props.role === 'result'}>{body()}</Show>
    </ToolMessageLayout>
  )
}
