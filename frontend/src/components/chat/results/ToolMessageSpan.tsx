import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ToolMessageSource } from './toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { createMemo, Show } from 'solid-js'
import { ToolMessage } from './ToolMessage'

/**
 * Wire the three sides of one tool span to the shared component.
 *
 * Every provider on the shared tool path resolves the SAME three sides — the row it
 * was called for, its span's opener, and its span's result — memoizes each one, and
 * renders `<ToolMessage>` once a row resolves. Four providers spelled that wiring
 * separately, and they did not agree: three guarded with `<Show>` and one with a
 * ternary, for the same question.
 *
 * Each caller supplies only what differs: how ITS payload becomes a row. The
 * accessors below hand each callback the parsed message of its own side, so a
 * provider never reads `context.sources` for a side the helper already resolved.
 *
 * A row that resolves to nothing draws nothing. A `source` callback that always
 * answers is welcome — the guard is then simply always true.
 */
export function ToolMessageSpan(props: {
  context?: RenderContext
  /** The row this renderer was called for, from the CURRENT message. */
  source: (parsed: ParsedMessageContent | undefined) => ToolMessageSource | undefined
  /** The span's opener, or undefined when the span states none. */
  request: (parsed: ParsedMessageContent) => ToolMessageSource | undefined
  /** The span's result, or undefined while the call still runs. */
  result: (parsed: ParsedMessageContent) => ToolMessageSource | undefined
}): JSX.Element {
  const source = createMemo(() => props.source(props.context?.sources?.current()))
  const request = createMemo(() => {
    const parsed = props.context?.sources?.request()
    return parsed ? props.request(parsed) : undefined
  })
  const result = createMemo(() => {
    const parsed = props.context?.sources?.result()
    return parsed ? props.result(parsed) : undefined
  })
  return (
    <Show when={source()}>
      {resolved => <ToolMessage source={resolved()} request={request()} result={result()} context={props.context} />}
    </Show>
  )
}
