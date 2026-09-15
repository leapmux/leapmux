import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ToolMessageSource } from '../../../results/toolPresentation'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo, For, Show } from 'solid-js'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { AgentResultBody } from '../../../results/agentResult'
import { ToolMessageSpan } from '../../../results/ToolMessageSpan'
import { piSubagentNotificationSources } from '../extractors/customMessage'
import { piToolMessageSource, piToolRow } from '../toolPresentation'
import { PiPlanRequest, PiPlanResult } from './plan'

interface RendererProps {
  parsed: unknown
  context?: RenderContext
}

/**
 * Resolve the native Pi events before the shared component renders the tool.
 *
 * Both sides of a span draw from the same model, so an opener and its result agree on
 * the icon, the title and the label without a second table to keep in step.
 */
function PiToolMessage(props: RendererProps): JSX.Element {
  const request = () => props.context?.sources?.request()
  const result = () => props.context?.sources?.result()
  const build = (payload: unknown, completion: MessageCompletion | undefined): ToolMessageSource | undefined => {
    const row = piToolRow(payload, request(), result(), completion)
    return row ? piToolMessageSource(row, completion) : undefined
  }
  return (
    <ToolMessageSpan
      context={props.context}
      source={parsed => build(props.parsed, parsed?.completion)}
      request={parsed => build(parsed.parentObject, parsed.completion)}
      result={parsed => build(parsed.parentObject, parsed.completion)}
    />
  )
}

/** The tool name of a Pi tool event, or an empty string for any other row. */
function piRowToolName(parsed: unknown): string {
  return isObject(parsed) ? pickString(parsed, 'toolName') : ''
}

/**
 * A Pi tool_execution_start row.
 *
 * A plan leaves the shared tool path, because it is not a tool body: every provider
 * draws a proposed plan through `MarkdownPlanLayout`.
 */
export function PiToolExecutionRenderer(props: RendererProps): JSX.Element {
  return (
    <Show when={isObject(props.parsed) && props.parsed}>
      {payload => (
        <Show
          when={piRowToolName(payload()) !== PI_TOOL.PlanComplete}
          fallback={<PiPlanRequest payload={payload()} context={props.context} />}
        >
          <PiToolMessage parsed={payload()} context={props.context} />
        </Show>
      )}
    </Show>
  )
}

/**
 * A Pi tool_execution_end row.
 *
 * A consolidated subagent notification classifies as a tool result and carries NO
 * tool call, so it draws its agent cards directly. The plan keeps its own layout for
 * the reason the request side gives.
 */
export function PiToolResultRenderer(props: RendererProps): JSX.Element {
  const notifications = createMemo(() => isObject(props.parsed) ? piSubagentNotificationSources(props.parsed) : null)
  return (
    <Show when={isObject(props.parsed) && props.parsed}>
      {payload => (
        <Show when={!notifications()} fallback={<For each={notifications()}>{source => <AgentResultBody source={source} context={props.context} />}</For>}>
          <Show
            when={piRowToolName(payload()) !== PI_TOOL.PlanComplete}
            fallback={<PiPlanResult payload={payload()} context={props.context} />}
          >
            <PiToolMessage parsed={payload()} context={props.context} />
          </Show>
        </Show>
      )}
    </Show>
  )
}
