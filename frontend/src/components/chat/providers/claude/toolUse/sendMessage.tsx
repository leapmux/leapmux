import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { SendMessageInput } from '~/types/toolMessages'
import { Show } from 'solid-js'
import { opensSubagentTranscript } from '~/stores/chatBackgroundTasks'
import { toolInputText, toolRecipientLink } from '../../../toolStyles.css'

/**
 * The recipient of a SendMessage call, as a link when it identifies a subagent of
 * this session and as plain text otherwise.
 *
 * The recipient is the one thing this card must lead with, and the generic tool
 * card cannot show it: `to` is not among the input keys the fallback title
 * inspects, so every SendMessage rendered as its message body with the
 * addressee invisible.
 *
 * `to` is the registry row key for a subagent of this session (Claude keys its
 * task registry by agent id). Every other recipient form -- a display name,
 * another session, a uds:/bridge:/did: address -- identifies no row here, so it stays
 * plain text rather than pretending to be somewhere the user can go.
 */
export function SendMessageRecipient(props: {
  to: string
  context?: RenderContext
}): JSX.Element {
  const row = () => props.context?.resolveBackgroundTaskRow?.(props.to)
  // Linkable only when the row owns a transcript. A shell row, or a subagent
  // whose provider never linked one, has no tab to open.
  const openable = () => {
    const item = row()
    if (!item || !opensSubagentTranscript(item))
      return undefined
    return props.context?.onOpenSubagent ? item : undefined
  }
  // The row's title is what the Background tasks list calls this subagent, so
  // the two show it the same way. The raw id is the honest fallback.
  const label = () => row()?.title || props.to

  return (
    <Show when={openable()} fallback={<span class={toolInputText}>{label()}</span>}>
      {item => (
        <button
          type="button"
          class={toolRecipientLink}
          data-testid="send-message-recipient"
          onClick={() => props.context?.onOpenSubagent?.(item())}
        >
          {label()}
        </button>
      )}
    </Show>
  )
}

/** Title for a SendMessage tool_use card: the recipient, linked where possible. */
export function renderSendMessageTitle(
  input: SendMessageInput,
  context?: RenderContext,
): JSX.Element | null {
  const to = typeof input.to === 'string' ? input.to.trim() : ''
  if (!to)
    return null
  return <SendMessageRecipient to={to} context={context} />
}
