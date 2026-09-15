import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ToolSpanSides } from '../../../results/ToolMessageSpan'
import type { ToolMessageSource } from '../../../results/toolPresentation'
import type { SpanRole } from '../../registry'
import type { ACPToolAdapter } from '../toolPresentation'
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
  const resolve = (tool: Record<string, unknown>, sides: ToolSpanSides, opener?: Record<string, unknown>, role?: SpanRole): ToolMessageSource => {
    const parsed = sides.own
    const resolved = resolveACPToolCall(tool, opener)
    const presentation = acpToolPresentation(resolved, props.adapter, parsed?.supplementalContent, parsed?.completion, { role, hasResult: !!sides.result })
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
      source={sides => resolve(props.toolUse, sides, sides.request?.parentObject, sides.role)}
      request={(sides) => {
        const tool = parsedACPToolCall(sides.own.parentObject)
        return tool ? resolve(tool, sides, undefined, 'opener') : undefined
      }}
      result={(sides) => {
        const tool = parsedACPToolCall(sides.own.parentObject)
        return tool ? resolve(tool, sides, props.toolUse, 'result') : undefined
      }}
    />
  )
}

export function acpToolCallUpdateRenderer(toolUse: Record<string, unknown>, context?: RenderContext, adapter?: ACPToolAdapter): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} adapter={adapter} />
}
