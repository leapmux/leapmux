import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { createMemo, Show } from 'solid-js'
import { MarkdownText } from '../../../messageRenderers'
import { zcodeAssistantText } from '../messageContent'

interface Props {
  parsed: unknown
  context?: RenderContext
}

/**
 * ZCode assistant text renderer.
 *
 * The completed reply arrives on a model-response `session.updated`, whose payload
 * carries the whole text under `content` -- there is no content-block array to walk,
 * because the app-server's part projection is not what a desktop-continuous
 * subscription delivers.
 *
 * There is no sibling reasoning renderer, and `classify` never answers
 * `assistant_thinking`. Reasoning reaches LeapMux only as a live `reasoning_delta`
 * stream, which the worker broadcasts and never persists as a row of its own.
 */
export function ZCodeAssistantMessage(props: Props): JSX.Element {
  const text = createMemo(() => zcodeAssistantText(props.parsed))
  return (
    <Show when={text()}>
      <MarkdownText text={text()} context={props.context} />
    </Show>
  )
}
