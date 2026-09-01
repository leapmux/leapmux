import type { Component } from 'solid-js'
import type { TitleState } from '~/hooks/createTitleState'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'

interface TitleInputProps {
  state: TitleState
}

/**
 * What the field says while it is EMPTY.
 *
 * A hint, not a sample value. It used to read "New Agent" / "New Terminal" /
 * "New Workspace", which look like titles the field would accept -- and the
 * field is empty only when submit is DISABLED, so the placeholder appeared
 * exactly when it offered a value the dialog refuses. It also had to be
 * threaded per dialog, which made ChangeBranchDialog write its Open-as
 * conditional a second time just to pick between two of them.
 */
const TITLE_PLACEHOLDER = 'Type a name'

/** The error node's id, so the input can point at it. */
const TITLE_ERROR_ID = 'title-input-error'

/**
 * The Title field shared by NewWorkspaceDialog, NewAgentDialog,
 * NewTerminalDialog and ChangeBranchDialog: a generated name, a button that
 * re-rolls it, and one "must not be empty" rule. Extracted so the four dialogs
 * cannot drift on the validation or on the layout.
 *
 * LabeledField draws the frame and states why the label is a plain `div`. That
 * div gives the input no accessible name, so it carries an explicit
 * `aria-label` -- the same answer LoadingMenu's `ariaLabel` gives for the menus
 * beside it.
 *
 * The error is LINKED to the input rather than only drawn beside it. This
 * field's error also disables Create, so a user who cannot see the red text
 * gets a dead button and no reason: `aria-invalid` marks the field, and the
 * `aria-describedby` link makes the message part of what a screen reader
 * announces for it.
 */
export const TitleInput: Component<TitleInputProps> = props => (
  <LabeledField
    label="Title"
    actions={(
      <RefreshButton
        onClick={() => props.state.regenerate()}
        title="Generate random name"
        data-testid="title-regenerate"
      />
    )}
    error={props.state.error()}
    errorId={TITLE_ERROR_ID}
  >
    <input
      type="text"
      aria-label="Title"
      aria-invalid={props.state.error() ? 'true' : undefined}
      aria-describedby={props.state.error() ? TITLE_ERROR_ID : undefined}
      data-testid="title-input"
      value={props.state.value()}
      onInput={e => props.state.setValue(e.currentTarget.value)}
      placeholder={TITLE_PLACEHOLDER}
    />
  </LabeledField>
)
