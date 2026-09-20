import type { Component } from 'solid-js'
import type { DialogPrompt } from '../model/controlPrompt'
import type { ContentProps } from './types'
import { Show, untrack } from 'solid-js'
import * as styles from '../ControlRequestBanner.css'
import { formatShortWait } from '../rendererUtils'
import { createControlChoice } from './types'

/** The answer state key the editor and the input share, so an edit survives a remount. */
export const DIALOG_TEXT_CHOICE_ID = 'dialog-text'

/**
 * The editor state of one dialog, held in the shared answer state.
 *
 * The content half and the actions half mount in two different slots for the SAME
 * request, and both read the text: the editor draws it and the send button submits
 * it. Holding it in the answer state is what lets each half remount without losing
 * what the reader typed -- including an empty value, which is a real answer.
 */
export function createDialogText(props: Pick<ContentProps, 'answerState'>, prefill: () => string | undefined) {
  // UNTRACKED, because the draft is the editor's STARTING value and not its current
  // one: re-reading it in a tracked scope would throw away what the reader typed
  // whenever the payload signal fired.
  return createControlChoice(() => props.answerState, DIALOG_TEXT_CHOICE_ID, untrack(prefill))
}

/**
 * The sentence a dialog with a deadline states, or null when it states none.
 *
 * {@link formatShortWait} states a sub-second deadline in milliseconds. Rounding to
 * whole seconds read "Auto-resolves in 0s if no response." for every deadline under
 * 500 ms, which tells the reader the dialog already expired.
 */
export function dialogTimeoutHint(dialog: DialogPrompt): string | null {
  return dialog.timeoutMs === undefined ? null : `Auto-resolves in ${formatShortWait(dialog.timeoutMs)} if no response.`
}

/**
 * One extension dialog: its heading, what it asks, and the editor an `editor`
 * variant answers with.
 *
 * The `input` variant draws its field in the ACTIONS half instead, beside the
 * buttons -- a one-line answer reads as part of the decision row, and putting it
 * here would separate the field from the button that sends it.
 */
export const DialogRequestContent: Component<ContentProps & { dialog: DialogPrompt }> = (props) => {
  const text = createDialogText(props, () => props.dialog.prefill)
  return (
    <>
      <div class={styles.controlBannerTitle}>{props.dialog.title}</div>
      <Show when={props.dialog.variant === 'confirm' && props.dialog.message}>
        <div class={styles.bannerReason}>{props.dialog.message}</div>
      </Show>
      <Show when={props.dialog.variant === 'input' && props.dialog.placeholder}>
        <div class={styles.bannerHint}>{`hint: ${props.dialog.placeholder}`}</div>
      </Show>
      <Show when={props.dialog.variant === 'editor'}>
        <textarea
          aria-label={props.dialog.title}
          value={text.choice() ?? ''}
          disabled={props.optionsDisabled}
          onInput={event => text.setChoice(event.currentTarget.value)}
          data-testid="pi-editor"
          rows={6}
          wrap="off"
          style={{ 'width': '100%', 'min-width': '0', 'max-width': '100%', 'max-height': '24rem', 'resize': 'vertical', 'font-family': 'var(--font-mono)' }}
        />
      </Show>
      <Show when={dialogTimeoutHint(props.dialog)}>
        {hint => <div class={styles.bannerHint}>{hint()}</div>}
      </Show>
    </>
  )
}
