import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import { createMemo } from 'solid-js'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { AgentRequestMessage } from '../../../results/AgentRequestMessage'
import { AgentResultBody } from '../../../results/agentResult'
import { ToolMessageLayout } from '../../../widgets/ToolMessageLayout'
import { piAgentRequest, piAgentResult } from '../extractors/agent'
import { piPairedRequest, piPairedResult } from '../extractors/toolCommon'
import { piWorkflowRequest, piWorkflowResult } from '../extractors/workflow'

interface Props { payload: Record<string, unknown>, context?: RenderContext }

export function PiAgentRequest(props: Props): JSX.Element {
  const source = createMemo(() => props.payload.toolName === PI_TOOL.SubagentWorkflow ? piWorkflowRequest(props.payload, undefined, props.context?.sources?.result()) : piAgentRequest(props.payload))
  return <AgentRequestMessage source={source()} hasResult={!!piPairedResult(props.payload, props.context?.sources?.result())} context={props.context} />
}

export function PiAgentResult(props: Props): JSX.Element {
  const request = () => piPairedRequest(props.payload, props.context?.sources?.request())
  const workflow = () => props.payload.toolName === PI_TOOL.SubagentWorkflow
  const source = createMemo(() => workflow() ? piWorkflowResult(props.payload, request()) : piAgentResult(props.payload, request()))
  const requestSource = createMemo(() => workflow() ? piWorkflowRequest(props.payload, request()) : piAgentRequest(props.payload, request()))
  return (
    <ToolMessageLayout role="result" hasRequest={!!request()} icon={Bot} toolName={requestSource().toolName} title={requestSource().description || 'Agent'} context={props.context} alwaysVisible>
      <AgentResultBody source={source()} context={props.context} />
    </ToolMessageLayout>
  )
}
