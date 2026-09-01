import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { labelRow } from '~/components/common/Dialog.css'
import { errorText } from '~/styles/shared.css'

interface LabeledFieldProps {
  /** The field's visible label. */
  label: JSX.Element
  /**
   * Controls that sit on the label row, right of the label: a refresh button,
   * a re-roll, a visibility toggle. Omit for a field with none.
   */
  actions?: JSX.Element
  /** Validation error to draw under the control, or null when acceptable. */
  error?: string | null
  /**
   * The error node's id, so the CONTROL can point at it with
   * `aria-describedby`. Required whenever `error` can be set, because a message
   * a screen reader never reaches is the same as no message for a user who
   * cannot see the red text.
   *
   * The control keeps its own `aria-invalid` and `aria-describedby`: this
   * wrapper owns the frame, and the control owns how it is announced.
   */
  errorId?: string
  /** Replaces the outer element's class. Defaults to no class. */
  class?: string
  children: JSX.Element
}

/**
 * The frame every dialog field shares: an outer element, a label row carrying
 * the label and any action buttons, the control, and the validation error.
 *
 * Seven components wrote this out by hand — WorkerSelector, ShellSelector,
 * AgentProviderSelector, DirectorySelector, TitleInput, SessionIdInput and
 * GitOptions' branch-name field — and they had already drifted: two drew the
 * error as a `div` and one as a `span`, so the message was block in one field
 * and inline in the next.
 *
 * The LABEL IS A PLAIN `div`, and that is the load-bearing part. A real
 * `<label>` element takes Oat's `label { font-size: var(--text-7); font-weight:
 * var(--font-medium) }` rule, which typesets it differently from every field
 * beside it — the defect that removing the Shell field's `<label>` fixed. Two
 * of these components restated that reason in prose; stating it once, here,
 * where the element is actually chosen, is what keeps the next field from
 * reaching for `<label>` again.
 *
 * A plain div is not a label, so it gives the control no accessible name. Each
 * control supplies its own (`aria-label`, or LoadingMenu's `ariaLabel`).
 */
export const LabeledField: Component<LabeledFieldProps> = props => (
  <div class={props.class}>
    <div class={labelRow}>
      {props.label}
      {props.actions}
    </div>
    {props.children}
    <Show when={props.error}>
      <div id={props.errorId} role="alert" class={errorText}>{props.error}</div>
    </Show>
  </div>
)
