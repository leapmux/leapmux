import type { Component } from 'solid-js'
import type { TitleState } from '~/hooks/createTitleState'
import { Show } from 'solid-js'
import { labelRow } from '~/components/common/Dialog.css'
import { RefreshButton } from '~/components/common/RefreshButton'
import { errorText } from '~/styles/shared.css'

interface TitleInputProps {
  state: TitleState
  placeholder: string
}

/**
 * The Title field shared by NewWorkspaceDialog, NewAgentDialog and
 * NewTerminalDialog: a generated name, a button that re-rolls it, and one
 * "must not be empty" rule. Extracted so the three dialogs cannot drift on the
 * validation or on the layout.
 *
 * The label is a plain `div`, matching WorkerSelector and SessionIdInput. A
 * real `<label>` element would take Oat's heavier `--text-7` / `--font-medium`
 * rule and typeset this label differently from "Worker" beside it, which is
 * the defect this change exists to remove from the Shell field.
 *
 * That leaves the input with no accessible name, so it carries an explicit
 * `aria-label` -- the same answer LoadingMenu's `ariaLabel` gives for the
 * menus in this dialog.
 */
export const TitleInput: Component<TitleInputProps> = props => (
  <div>
    <div class={labelRow}>
      Title
      <RefreshButton
        onClick={() => props.state.regenerate()}
        title="Generate random name"
        data-testid="title-regenerate"
      />
    </div>
    <input
      type="text"
      aria-label="Title"
      data-testid="title-input"
      value={props.state.value()}
      onInput={e => props.state.setValue(e.currentTarget.value)}
      placeholder={props.placeholder}
    />
    <Show when={props.state.error()}>
      <div class={errorText}>{props.state.error()}</div>
    </Show>
  </div>
)
