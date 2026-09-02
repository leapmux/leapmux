import type { Component } from 'solid-js'
import type { SessionIdState } from '~/hooks/createSessionIdState'
import { RESUME_SESSION_ERROR_ID, RESUME_SESSION_LABEL, resumeHandlePlaceholder } from '~/components/shell/resumeSession'

interface SessionIdInputProps {
  state: SessionIdState
}

/**
 * The typed control of the resume field: a text box for a session handle.
 *
 * `ResumeSessionField` mounts it as the FALLBACK, where the worker offered no
 * session list. It renders the control alone, because that field owns the
 * frame — the label row, the refresh button and the error node — and shows the
 * same frame around its menu.
 *
 * The placeholder states both shapes for a provider whose session is a FILE,
 * because such a provider takes either one and the field used to invite the
 * wrong one: it said "Session ID" for every provider, and the worker then
 * refused a Pi session ID with "path must be absolute". It comes from
 * `resumeHandlePlaceholder`, which is declared beside the menu row that opens
 * this box so the two cannot name different sets of shapes.
 *
 * `LabeledField`'s label is a plain `div`, so the input has no accessible name
 * of its own and carries an explicit `aria-label`. Without it the PLACEHOLDER
 * becomes the last-resort name, so a screen-reader user hears "Session ID" and
 * never the visible label — and the announced name then CHANGES with the
 * selected provider.
 */
export const SessionIdInput: Component<SessionIdInputProps> = props => (
  <input
    type="text"
    aria-label={RESUME_SESSION_LABEL}
    aria-invalid={props.state.error() ? 'true' : undefined}
    aria-describedby={props.state.error() ? RESUME_SESSION_ERROR_ID : undefined}
    value={props.state.value()}
    onInput={e => props.state.setValue(e.currentTarget.value)}
    placeholder={resumeHandlePlaceholder(props.state.isFilePath())}
  />
)
