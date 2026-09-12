import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ToolMetadataItem } from './ToolMetadata'
import Bot from 'lucide-solid/icons/bot'
import { Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolResultPrompt } from '../toolStyles.css'
import { renderAgentTitle } from '../toolTitleRenderers'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { ToolMetadata } from './ToolMetadata'

export interface AgentRequestSource {
  toolName: string
  description: string
  agentType?: string
  prompt: string
  promptLabel?: string
  promptFormat?: 'markdown' | 'pre'
  metadata?: ToolMetadataItem[]
}

/** Show pending instructions. Keep completed requests compact, with their prompt available on expansion. */
export function AgentRequestMessage(props: { source: AgentRequestSource, hasResult?: boolean, context?: RenderContext, children?: JSX.Element }): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.AGENT_PROMPT)
  const hasPrompt = () => props.source.prompt.trim() !== ''
  const promptLabel = () => props.source.promptLabel || 'Prompt'
  const hasMetadata = () => (props.source.metadata?.length ?? 0) > 0
  const longPrompt = () => hasMoreLinesThan(props.source.prompt, COLLAPSED_RESULT_ROWS)
  const { copied, copy } = useCopyButton(() => props.source.prompt)
  return (
    <ToolMessageLayout
      role="request"
      icon={Bot}
      toolName={props.source.toolName}
      title={renderAgentTitle(props.source.description.trim() || props.source.toolName.trim() || 'Agent', props.source.agentType?.trim())}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={(hasPrompt() || hasMetadata()) && (props.hasResult || longPrompt()) ? () => setExpanded(value => !value) : undefined}
      expandLabel={hasPrompt() ? `Show ${promptLabel().toLowerCase()}` : 'Show details'}
      headerActions={{ onCopyContent: hasPrompt() ? copy : undefined, contentCopied: copied(), copyContentLabel: `Copy ${promptLabel().toLowerCase()}` }}
      alwaysVisible
    >
      <Show when={!props.hasResult || expanded()}><ToolMetadata items={props.source.metadata} /></Show>
      <Show when={hasPrompt() && (!props.hasResult || expanded())}>
        <div class={toolResultPrompt}>{promptLabel()}</div>
        <CollapsibleContent kind={props.source.promptFormat === 'pre' ? 'pre' : 'markdown-tool-result'} text={props.source.prompt} isCollapsed={!expanded() && longPrompt()} context={props.context} />
      </Show>
      {props.children}
    </ToolMessageLayout>
  )
}
