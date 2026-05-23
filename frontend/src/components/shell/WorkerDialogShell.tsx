import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { errorText } from '~/styles/shared.css'

export interface WorkerDialogShellProps {
  title: string
  /**
   * Submit-in-flight flag. Drives the Dialog `busy` overlay; the footer
   * reflects it on its own buttons.
   */
  submitting: boolean
  /** User-friendly error text, rendered between the body and footer. */
  error: string | null | undefined
  onClose: () => void
  /**
   * Drops the tall + wide creation-dialog layout when the caller's
   * content is small enough not to need it (Change Branch / Delete
   * Branch). Defaults to false — creation dialogs (NewAgent /
   * NewTerminal / NewWorkspace) get the full size with no extra prop.
   */
  compact?: boolean
  children: JSX.Element
  /**
   * Action shape. Pass a {@link DialogFormFooter} for the standard
   * Cancel + Submit pair, or any custom JSX for a different action
   * layout (e.g. DeleteBranchDialog's Push + Delete combo).
   */
  footer: JSX.Element
  /**
   * When set, the shell wraps body + footer in a `<form>` so Enter-key
   * in any body input triggers the form's submit. Pair with a
   * {@link DialogFormFooter} (which renders a `button[type=submit]`).
   * Custom-footer dialogs whose actions are all explicit buttons
   * (`type="button"`) omit this — no form wrap, no incidental submit.
   */
  onSubmit?: (e: Event) => void
}

/**
 * Shared scaffold for every worker-targeted dialog. Owns the Dialog
 * wrapper, the body section, and the error row; slots the caller's
 * `footer` (either a {@link DialogFormFooter} for the standard
 * Cancel/Submit shape or any custom JSX). When `onSubmit` is provided,
 * the body + footer are wrapped in a `<form>` so Enter-key submit
 * reaches the footer's submit button.
 */
export const WorkerDialogShell: Component<WorkerDialogShellProps> = (props) => {
  const body = () => (
    <>
      <section>
        <div class="vstack gap-4">
          {props.children}
        </div>
        <Show when={props.error}>
          <div class={errorText}>{props.error}</div>
        </Show>
      </section>
      <footer>{props.footer}</footer>
    </>
  )
  return (
    <Dialog
      title={props.title}
      tall={!props.compact}
      wide={!props.compact}
      busy={props.submitting}
      onClose={() => props.onClose()}
    >
      <Show when={props.onSubmit} fallback={body()}>
        <form onSubmit={e => props.onSubmit!(e)}>{body()}</form>
      </Show>
    </Dialog>
  )
}

export interface DialogFormFooterProps {
  /** Submit button label when idle. */
  submitLabel: string
  /** Submit button label while submitting (defaults to `${submitLabel}...`). */
  submittingLabel?: string
  /** Submit-in-flight flag; disables both buttons + shows the spinner. */
  submitting: boolean
  /** Additional submit-gating (validation, missing required fields). */
  submitDisabled?: boolean
  /** Forwarded to the Cancel button's onClick. */
  onClose: () => void
}

/**
 * Default Cancel + Submit footer with the spinner-in-submit pattern used
 * by every creation dialog. Submission is wired through the surrounding
 * `<form onSubmit>` in {@link WorkerDialogShell}, so this component
 * doesn't need an onSubmit prop of its own — clicking the submit button
 * (or pressing Enter inside any body input) reaches the shell's form
 * handler.
 */
export const DialogFormFooter: Component<DialogFormFooterProps> = (props) => {
  const submittingLabel = () => props.submittingLabel ?? `${props.submitLabel}...`
  return (
    <>
      <button type="button" class="outline" disabled={props.submitting} onClick={() => props.onClose()}>
        Cancel
      </button>
      <button type="submit" disabled={props.submitting || props.submitDisabled}>
        <Show when={props.submitting}>
          <Spinner />
        </Show>
        {props.submitting ? submittingLabel() : props.submitLabel}
      </button>
    </>
  )
}
