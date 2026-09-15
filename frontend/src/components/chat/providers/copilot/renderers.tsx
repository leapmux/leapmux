import type { JSX } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import type { ToolMessageSource } from '../../results/toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickString } from '~/lib/jsonPick'
import { MarkdownText, ThinkingMessage } from '../../messageRenderers'
import { ToolMessageSpan } from '../../results/ToolMessageSpan'
import { copilotEvent } from './protocol'
import { copilotToolPresentation, copilotToolRow } from './toolPresentation'

/** The text of one assistant or reasoning event. */
export function copilotEventText(parsed: unknown): string {
  return pickString(copilotEvent(parsed)?.data, 'content')
}

export function copilotAssistantRenderer(parsed: unknown, context?: RenderContext): JSX.Element | null {
  const text = copilotEventText(parsed)
  return text ? <MarkdownText text={text} context={context} /> : null
}

export function copilotReasoningRenderer(parsed: unknown, context?: RenderContext): JSX.Element | null {
  const text = copilotEventText(parsed)
  return text ? <ThinkingMessage text={text} context={context} /> : null
}

/**
 * Build one tool source, or undefined for a row that is not a tool event.
 *
 * `request` is the paired `tool.execution_start`, which a result row needs for the
 * tool name and the arguments its own event omits.
 */
function copilotToolSource(
  parsed: unknown,
  spanType: string | undefined,
  request: ParsedMessageContent | undefined,
  completion: ParsedMessageContent['completion'],
): ToolMessageSource | undefined {
  const row = copilotToolRow(parsed, spanType, request, completion)
  if (!row)
    return undefined
  const presentation = copilotToolPresentation(row)
  return {
    id: row.toolCallId,
    role: row.finished ? 'result' : 'request',
    status: row.status,
    presentation,
    // Copilot's rich content always rides a rendered body: a Model Context Protocol
    // body when the result is content blocks, and the command body's own additional
    // content when it is a shell result. The shared image list would therefore draw
    // each image a SECOND time. The provider's toolResultImages hook still reports
    // every image, which is what the gallery reads.
    images: [],
  }
}

/** Resolve the native events before the shared component renders the tool. */
export function CopilotToolMessage(props: { parsed: unknown, context?: RenderContext }): JSX.Element {
  return (
    <ToolMessageSpan
      context={props.context}
      source={sides => copilotToolSource(props.parsed, props.context?.spanType, sides.request, sides.own?.completion)}
      // The opener resolves against ITSELF, which is what an opener's own row needs.
      request={sides => copilotToolSource(sides.own.parentObject, props.context?.spanType, sides.own, sides.own.completion)}
      result={sides => copilotToolSource(sides.own.parentObject, props.context?.spanType, sides.request, sides.own.completion)}
    />
  )
}

export function copilotToolRenderer(parsed: unknown, context?: RenderContext): JSX.Element {
  return <CopilotToolMessage parsed={parsed} context={context} />
}
