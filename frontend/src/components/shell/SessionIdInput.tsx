import type { Component } from 'solid-js'
import type { SessionIdState } from '~/hooks/createSessionIdState'
import { LabeledField } from '~/components/common/LabeledField'

interface SessionIdInputProps {
  state: SessionIdState
}

/**
 * The visible label of the resume field, reused as the accessible name of
 * whichever control the field is showing.
 *
 * Exported because `ResumeSessionField` swaps between this input and
 * `SessionSelect`, and the two must answer to ONE name: a screen-reader user
 * whose label changed when the session list arrived would be told the field
 * became a different field.
 */
export const RESUME_SESSION_LABEL = 'Resume an existing session'

/**
 * The error node's id, so the input can point at it.
 *
 * Exported and shared with `ResumeSessionField`, which shows the same error
 * above its menu. The two controls never render together, so one id serves
 * both and cannot collide.
 */
export const RESUME_SESSION_ERROR_ID = 'session-id-error'

/**
 * Optional session-id input used by NewWorkspaceDialog and NewAgentDialog
 * to resume an existing agent session. The two dialogs share the same
 * label, validation, and error styling — extracted here so the per-keystroke
 * validation lives in one place.
 *
 * The placeholder states both shapes for a provider whose session is a FILE,
 * because such a provider takes either one and the field used to invite the
 * wrong one: it said "Session ID" for every provider, and the worker then
 * refused a Pi session ID with "path must be absolute".
 *
 * LabeledField's label is a plain `div`, so the input has no accessible name of
 * its own and carries an explicit `aria-label`. Without it the PLACEHOLDER
 * becomes the last-resort name, so a screen-reader user hears "Session ID" and
 * never the visible label — and the announced name then CHANGES with the
 * selected provider.
 */
export const SessionIdInput: Component<SessionIdInputProps> = props => (
  <LabeledField
    label={RESUME_SESSION_LABEL}
    error={props.state.error()}
    errorId={RESUME_SESSION_ERROR_ID}
  >
    <input
      type="text"
      aria-label={RESUME_SESSION_LABEL}
      aria-invalid={props.state.error() ? 'true' : undefined}
      aria-describedby={props.state.error() ? RESUME_SESSION_ERROR_ID : undefined}
      value={props.state.value()}
      onInput={e => props.state.setValue(e.currentTarget.value)}
      placeholder={props.state.isFilePath() ? 'Session ID or file path' : 'Session ID'}
    />
  </LabeledField>
)
