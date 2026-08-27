import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { errorText, successText } from '~/styles/shared.css'

/**
 * One outcome of one settings action: what happened, and whether it worked.
 *
 * `null` means "nothing to report yet", which is what a handler writes before
 * it starts.
 */
export interface StatusMessage {
  type: 'success' | 'error'
  text: string
}

/**
 * Renders a StatusMessage, and nothing at all when there is none.
 *
 * The colour follows `type`, so a caller never picks the class itself. Nine
 * copies of the same ternary lived across the three settings surfaces, and a
 * green "Failed to..." is what a copy that drifts looks like.
 */
export const StatusLine: Component<{ message: StatusMessage | null | undefined }> = props => (
  <Show when={props.message}>
    {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
  </Show>
)
