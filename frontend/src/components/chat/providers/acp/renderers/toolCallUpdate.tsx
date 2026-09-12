import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ToolMessageSource } from '../../../results/toolPresentation'
import type { SpanRole } from '../../registry'
import type { ACPToolAdapter } from '../toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { createMemo } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { ToolMessage } from '../../../results/ToolMessage'
import { acpImagesFromToolCall } from '../extractors/image'
import { acpToolFinished, acpToolPresentation, parsedACPToolCall, resolveACPToolCall } from '../toolPresentation'

/** Resolve native ACP fields before the shared component renders the tool. */
export function ToolCallUpdateMessage(props: {
  toolUse: Record<string, unknown>
  context?: RenderContext
  adapter?: ACPToolAdapter
}): JSX.Element {
  // Where the resolved row sits in its span. A provider needs it to place something
  // exactly once across the two rows of one tool call, because the tool's own fields no
  // longer separate them once `resolveACPToolCall` merges the opener.
  const hasResult = () => !!props.context?.sources?.result()
  const resolve = (tool: Record<string, unknown>, parsed?: ParsedMessageContent, opener?: Record<string, unknown>, role?: SpanRole): ToolMessageSource => {
    const resolved = resolveACPToolCall(tool, opener)
    const presentation = acpToolPresentation(resolved, props.adapter, parsed?.supplementalContent, parsed?.completion, { role, hasResult: hasResult() })
    return {
      id: pickString(resolved, 'toolCallId'),
      role: acpToolFinished(resolved, parsed?.completion) ? 'result' : tool.sessionUpdate === 'tool_call' ? 'request' : 'update',
      status: pickString(resolved, 'status'),
      presentation,
      images: acpImagesFromToolCall({ ...resolved, rawInput: presentation.input }),
    }
  }
  const request = createMemo(() => {
    const parsed = props.context?.sources?.request()
    const tool = parsedACPToolCall(parsed?.parentObject)
    return tool ? resolve(tool, parsed, undefined, 'opener') : undefined
  })
  const source = createMemo(() => resolve(props.toolUse, props.context?.sources?.current(), props.context?.sources?.request()?.parentObject, props.context?.sources?.role()))
  const result = createMemo(() => {
    const parsed = props.context?.sources?.result()
    const tool = parsedACPToolCall(parsed?.parentObject)
    return tool ? resolve(tool, parsed, props.toolUse, 'result') : undefined
  })
  return <ToolMessage source={source()} request={request()} result={result()} context={props.context} />
}

export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, context?: RenderContext, adapter?: ACPToolAdapter): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} adapter={adapter} />
}
