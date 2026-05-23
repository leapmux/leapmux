import type { Component } from 'solid-js'
import type { SessionIdState } from '~/hooks/createSessionIdState'
import { Show } from 'solid-js'
import { labelRow } from '~/components/common/Dialog.css'
import { errorText } from '~/styles/shared.css'

interface SessionIdInputProps {
  state: SessionIdState
}

/**
 * Optional session-id input used by NewWorkspaceDialog and NewAgentDialog
 * to resume an existing agent session. The two dialogs share the same
 * label, validation, and error styling — extracted here so the per-keystroke
 * validation lives in one place.
 */
export const SessionIdInput: Component<SessionIdInputProps> = (props) => {
  return (
    <div>
      <div class={labelRow}>Resume an existing session</div>
      <input
        type="text"
        value={props.state.value()}
        onInput={e => props.state.setValue(e.currentTarget.value)}
        placeholder="Session ID"
      />
      <Show when={props.state.error()}>
        <span class={errorText}>{props.state.error()}</span>
      </Show>
    </div>
  )
}
