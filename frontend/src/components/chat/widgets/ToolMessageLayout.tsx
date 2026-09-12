import type { ComponentProps, JSX } from 'solid-js'
import { children, Show, splitProps } from 'solid-js'
import { ToolUseLayout } from './ToolUseLayout'

/** A paired result uses the request's header. A standalone result keeps its own header. */
export function ToolMessageLayout(props: ComponentProps<typeof ToolUseLayout> & {
  role: 'request' | 'result'
  hasRequest?: boolean
}): JSX.Element {
  const [message, layout] = splitProps(props, ['role', 'hasRequest', 'children'])
  const body = children(() => message.children)
  return (
    <Show when={message.role !== 'result' || !message.hasRequest} fallback={body()}>
      <ToolUseLayout {...layout}>{body()}</ToolUseLayout>
    </Show>
  )
}
