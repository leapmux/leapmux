import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { McpToolCallSource } from './mcpToolCall'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Wrench from 'lucide-solid/icons/wrench'
import { createMemo, Show } from 'solid-js'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { retainedOutcome } from '../providers/registry'
import { toolOutcomeLabel } from '../toolOutcomeLabel'
import { renderMcpTitle } from '../toolTitleRenderers'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { McpToolCallBody, mcpToolCallCollapsible, mcpToolCallDisplayName } from './mcpToolCall'
import { ToolOutcomeHeader } from './ToolStatusHeader'

/** Keep the request header separate from the argument and result body. */
export function McpToolMessage(props: {
  source: McpToolCallSource
  role: 'request' | 'result'
  hasRequest?: boolean
  failureLabel?: string
  /**
   * Rich content that accompanies the result, from `ToolPresentation.additionalContent`.
   * The row draws it below the result, and `toolPresentationMeta` already counts it in
   * both the Copy text and the collapsible answer -- so a row that dropped it promised
   * content the reader could not see.
   */
  additionalContent?: McpToolCallSource
  context?: RenderContext
}): JSX.Element {
  // retainedOutcome is the one reading of the completion column, so this row cannot
  // disagree with the tool presentation about whether the same row ended badly.
  const outcome = () => retainedOutcome(props.context?.sources?.current()?.completion)
  const source = createMemo(() => outcome() === 'interrupted' || outcome() === 'failed'
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
  // The accompanying content is numbered FIRST, because `Provider.toolResultImages`
  // lists it first (`acpToolResultImages` returns `[...additional, ...primary]`), and
  // an image tab resolves index N against that list.
  const additionalImageCount = () => props.additionalContent?.content.filter(item => item.type === 'image').length ?? 0
  // Both bodies answer, so the toggle appears when EITHER holds more than it shows.
  // `toolOutputCollapsible` reads the same pair for the toolbar.
  const collapsible = () => mcpToolCallCollapsible(props.source)
    || (props.additionalContent !== undefined && mcpToolCallCollapsible(props.additionalContent))
  const body = () => (
    <>
      <ToolOutcomeHeader
        when={source().status === 'failed'}
        icon={CircleAlert}
        title={outcome() === 'interrupted' ? toolOutcomeLabel('interrupted') : props.failureLabel || toolOutcomeLabel('failed')}
        context={props.context}
      />
      <McpToolCallBody source={source()} context={props.context} expanded={expanded} indexOffset={additionalImageCount()} />
      <Show when={props.additionalContent}>
        {additional => <McpToolCallBody source={additional()} context={props.context} expanded={expanded} />}
      </Show>
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
      onToggleExpand={props.role === 'result' && collapsible() ? () => setExpanded(value => !value) : undefined}
      alwaysVisible
    >
      <Show when={props.role === 'result'}>{body()}</Show>
    </ToolMessageLayout>
  )
}
