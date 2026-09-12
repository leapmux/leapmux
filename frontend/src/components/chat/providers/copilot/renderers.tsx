import type { JSX } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import type { ToolMessageSource } from '../../results/toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { createMemo } from 'solid-js'
import { pickString } from '~/lib/jsonPick'
import { MarkdownText, ThinkingMessage } from '../../messageRenderers'
import { ToolMessage } from '../../results/ToolMessage'
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
export function CopilotToolMessage(props: { parsed: unknown, context?: RenderContext }): JSX.Element | null {
  const request = createMemo(() => {
    const parsed = props.context?.sources?.request()
    return parsed ? copilotToolSource(parsed.parentObject, props.context?.spanType, parsed, parsed.completion) : undefined
  })
  const source = createMemo(() => {
    const current = props.context?.sources?.current()
    return copilotToolSource(props.parsed, props.context?.spanType, props.context?.sources?.request(), current?.completion)
  })
  const result = createMemo(() => {
    const parsed = props.context?.sources?.result()
    return parsed ? copilotToolSource(parsed.parentObject, props.context?.spanType, props.context?.sources?.request(), parsed.completion) : undefined
  })
  const model = createMemo(() => source())
  return (
    <>
      {model() ? <ToolMessage source={model()!} request={request()} result={result()} context={props.context} /> : null}
    </>
  )
}

export function copilotToolRenderer(parsed: unknown, context?: RenderContext): JSX.Element {
  return <CopilotToolMessage parsed={parsed} context={context} />
}
