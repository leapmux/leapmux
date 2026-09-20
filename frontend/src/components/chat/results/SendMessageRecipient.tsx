import type { JSX } from 'solid-js'
import type { SubagentNavigation } from '../renderContext'
import { Show } from 'solid-js'
import { opensSubagentTranscript } from '~/stores/chatBackgroundTasks'
import { toolInputText, toolRecipientLink } from '../toolStyles.css'

/**
 * The recipient of a message one agent sent another, as a link when it identifies a
 * subagent of this session and as plain text otherwise.
 *
 * The recipient is the one thing such a row must lead with, and the shared title
 * renderers cannot compose it: the addressee is neither a path nor a pattern,
 * and a row that fell back to the generic header showed the message body with
 * the addressee invisible.
 *
 * `to` is the registry row key for a subagent of this session. Every other
 * recipient form -- a display name, another session, a `uds:`/`bridge:`/`did:`
 * address -- identifies no row here, so it stays plain text rather than
 * pretending to be somewhere the reader can go.
 */
export function SendMessageRecipient(props: {
  to: string
  /**
   * Resolving the recipient's row and opening its transcript. Assembled where the
   * session's navigation is in scope; the background-task registry itself never
   * reaches this component.
   */
  navigation: SubagentNavigation | undefined
}): JSX.Element {
  const row = () => props.navigation?.row(props.to)
  // Linkable only when the row owns a transcript. A shell row, or a subagent
  // whose provider never linked one, has no tab to open.
  const openable = () => {
    const item = row()
    if (!item || !opensSubagentTranscript(item))
      return undefined
    return props.navigation?.open ? item : undefined
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
          onClick={() => props.navigation?.open?.(item())}
        >
          {label()}
        </button>
      )}
    </Show>
  )
}
