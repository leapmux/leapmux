import type { Component } from 'solid-js'
import type { DialogPrompt } from '../model/controlPrompt'
import type { ActionsProps, ContentProps } from './types'
import { createMemo, Show, untrack } from 'solid-js'
import * as styles from '../ControlRequestBanner.css'
import { formatShortWait } from '../rendererUtils'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { invokeControlAction } from './controlResponseError'
import { createControlChoice, sendResponse } from './types'

/**
 * How a provider answers an extension dialog. Each builder returns the provider's
 * own envelope for one answer, and the shared dialog actions send it.
 *
 * A provider whose `extractControl` returns a `dialog` states this beside it. A
 * dialog of a provider that states none reaches the banner's generic Allow/Deny
 * pair, which is a way out of the request rather than the right words for it.
 */
export interface DialogResponder {
  /** The answer to a `confirm`: the reader approved or denied. */
  confirm: (requestId: string, confirmed: boolean) => unknown
  /** The text that the reader sent for an `input` or an `editor`, the empty text included. */
  value: (requestId: string, value: string) => unknown
  /** The reader dismissed the dialog. */
  cancel: (requestId: string) => unknown
}

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
          data-testid="dialog-editor"
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

/** The two buttons of one dialog variant, and what each one sends. */
interface DialogButtons {
  negativeLabel: string
  negative: () => unknown
  positiveLabel: string
  positive: () => unknown
}

/**
 * The decision row of one extension dialog.
 *
 * A `confirm` offers Deny and Approve. An `input` and an `editor` offer Cancel and
 * Send, and Send submits the text as it stands, the empty text included: a runtime
 * tells an empty answer apart from a cancellation. The `input` field sits in this
 * row beside the buttons; the `editor` draws in the content half
 * (`DialogRequestContent`). Both halves read the text through the SAME answer-state
 * key, so what the reader typed reaches the send.
 */
export const DialogRequestActions: Component<ActionsProps & { dialog: DialogPrompt, responder: DialogResponder }> = (props) => {
  const requestId = () => props.request.requestId
  const text = createDialogText(props, () => props.dialog.prefill)
  const value = () => text.choice() ?? ''
  const send = (response: unknown) => sendResponse(props.onRespond, response)
  const sendValue = () => send(props.responder.value(requestId(), value()))

  const buttons = createMemo<DialogButtons>(() => props.dialog.variant === 'confirm'
    ? {
        negativeLabel: 'Deny',
        negative: () => props.responder.confirm(requestId(), false),
        positiveLabel: 'Approve',
        positive: () => props.responder.confirm(requestId(), true),
      }
    : {
        negativeLabel: 'Cancel',
        negative: () => props.responder.cancel(requestId()),
        positiveLabel: 'Send',
        positive: () => props.responder.value(requestId(), value()),
      })

  return (
    <ControlDecisionFooter
      hasEditorContent={false}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: buttons().negativeLabel, testId: 'control-deny-btn', onSelect: () => send(buttons().negative()) }}
      positiveAction={{ label: buttons().positiveLabel, testId: 'control-allow-btn', onSelect: () => send(buttons().positive()) }}
      leading={(
        <Show when={props.dialog.variant === 'input'}>
          <input
            type="text"
            aria-label={props.dialog.title}
            placeholder={props.dialog.placeholder}
            value={value()}
            onInput={event => text.setChoice(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                invokeControlAction(sendValue)
              }
            }}
            data-testid="dialog-input"
            style={{ 'flex': '1 1 200px', 'min-width': '0', 'max-width': '100%' }}
          />
        </Show>
      )}
    />
  )
}
