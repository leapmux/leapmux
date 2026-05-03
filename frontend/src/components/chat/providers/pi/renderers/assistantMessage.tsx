import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { createMemo, Show } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { MarkdownText, ThinkingMessage } from '../../../messageRenderers'
import { piContentText } from '../messageContent'

interface Props {
  parsed: unknown
  context?: RenderContext
}

/**
 * Pi assistant text renderer. Walks the `message.content[]` array of a
 * `message_end` event and joins all `{type:'text', text}` blocks into
 * markdown.
 */
export function PiAssistantMessage(props: Props): JSX.Element {
  const text = createMemo((): string => {
    const obj = isObject(props.parsed) ? props.parsed : null
    return obj ? piContentText(obj, 'text') : ''
  })
  return (
    <Show when={text()}>
      <MarkdownText text={text()} />
    </Show>
  )
}

/**
 * Pi thinking renderer. Pulls `{type:'thinking', thinking}` blocks out of a
 * `message_end` event so we can render them under the shared collapsed
 * "Thinking" bubble.
 */
export function PiAssistantThinking(props: Props): JSX.Element {
  const text = createMemo((): string => {
    const obj = isObject(props.parsed) ? props.parsed : null
    return obj ? piContentText(obj, 'thinking') : ''
  })
  return <ThinkingMessage text={text()} context={props.context} />
}
