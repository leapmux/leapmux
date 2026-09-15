import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ToolMessageSource } from '../../../results/toolPresentation'
import type { SpanRole } from '../../registry'
import type { ACPToolAdapter } from '../toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { toolRowStatus } from '~/components/chat/results/toolRowStatus'
import { pickString } from '~/lib/jsonPick'
import { ToolMessageSpan } from '../../../results/ToolMessageSpan'
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
      status: toolRowStatus(pickString(resolved, 'status')),
      presentation,
      images: acpImagesFromToolCall({ ...resolved, rawInput: presentation.input }),
    }
  }
  return (
    <ToolMessageSpan
      context={props.context}
      // ACP always resolves a row, so the helper's guard is always true here.
      source={parsed => resolve(props.toolUse, parsed, props.context?.sources?.request()?.parentObject, props.context?.sources?.role())}
      request={(parsed) => {
        const tool = parsedACPToolCall(parsed.parentObject)
        return tool ? resolve(tool, parsed, undefined, 'opener') : undefined
      }}
      result={(parsed) => {
        const tool = parsedACPToolCall(parsed.parentObject)
        return tool ? resolve(tool, parsed, props.toolUse, 'result') : undefined
      }}
    />
  )
}

export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, context?: RenderContext, adapter?: ACPToolAdapter): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} adapter={adapter} />
}
