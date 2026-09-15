import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { SpanRole } from '../providers/registry'
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
 * Each caller supplies only what differs: how ITS payload becomes a row. Every
 * callback receives ALL THREE parsed sides plus the span role, so a provider never
 * reads `context.sources` for a side this helper already resolved.
 *
 * All three, not just its own. Every caller needs a second side: ACP needs the
 * request's parent object and the role inside `source`, Copilot needs the request
 * inside `source` and `result`, ZCode needs all three, and Pi needs the request and
 * the result. A callback that took only its own side sent each of them back to
 * `context.sources`, which is the wiring this helper exists to own -- and each read
 * there is a fresh call outside the memo that resolved it here.
 *
 * A row that resolves to nothing draws nothing. A `source` callback that always
 * answers is welcome — the guard is then simply always true.
 */
/** Every side of one tool span, resolved once, plus the role of the current row. */
export interface ToolSpanSides {
  /** The parsed message of the side the callback is building. */
  own: ParsedMessageContent | undefined
  /** The message this renderer was called for. Equals `own` in the source callback. */
  current: ParsedMessageContent | undefined
  /** The span's opener, or undefined when the span states none. */
  request: ParsedMessageContent | undefined
  /** The span's result, or undefined while the call still runs. */
  result: ParsedMessageContent | undefined
  role: SpanRole
}

export function ToolMessageSpan(props: {
  context?: RenderContext
  /** The row this renderer was called for, from the CURRENT message. */
  source: (sides: ToolSpanSides) => ToolMessageSource | undefined
  /** The span's opener. Called only when the span states one. */
  request: (sides: ToolSpanSides & { own: ParsedMessageContent }) => ToolMessageSource | undefined
  /** The span's result. Called only once the call finishes. */
  result: (sides: ToolSpanSides & { own: ParsedMessageContent }) => ToolMessageSource | undefined
}): JSX.Element {
  const sides = createMemo<Omit<ToolSpanSides, 'own'>>(() => ({
    current: props.context?.sources?.current(),
    request: props.context?.sources?.request(),
    result: props.context?.sources?.result(),
    role: props.context?.sources?.role() ?? 'other',
  }))
  const source = createMemo(() => props.source({ ...sides(), own: sides().current }))
  const request = createMemo(() => {
    const own = sides().request
    return own ? props.request({ ...sides(), own }) : undefined
  })
  const result = createMemo(() => {
    const own = sides().result
    return own ? props.result({ ...sides(), own }) : undefined
  })
  return (
    <Show when={source()}>
      {resolved => <ToolMessage source={resolved()} request={request()} result={result()} context={props.context} />}
    </Show>
  )
}
