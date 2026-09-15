import type { Component } from 'solid-js'
import { createSignal, createUniqueId, onCleanup, Show } from 'solid-js'
import { MarkdownEditor } from '~/components/chat/markdownEditor/MarkdownEditor'
import { formatNumber } from '~/components/chat/rendererUtils'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { formatErrorMessage } from '~/lib/errors'
import { utf8ByteLength } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './SetGoalDialog.css'

export interface SetGoalDialogProps {
  /** The current objective to edit, or an empty value for the first goal. */
  initialObjective?: string
  /**
   * Deliver the objective. THROW to keep the dialog open with the reason in its refusal
   * slot; the provider states its own words there. Resolving closes the dialog.
   */
  onSubmit: (objective: string) => Promise<void>
  onClose: () => void
}

/** Show the remaining byte budget when the objective reaches 90% of the limit. */
const BUDGET_NOTICE_RATIO = 0.9

/**
 * The editor grows from 120px to 320px, then scrolls.
 * `pinnedHeight` would keep the editor at 120px after its content exceeds that height.
 */
const EDITOR_MIN_HEIGHT_PX = 120
const EDITOR_MAX_HEIGHT_PX = 320

/**
 * Edit the session objective with the same Markdown editor and keyboard preferences as the composer.
 * The dialog gives paragraphs more space than the 360px Goals & To-dos popover.
 * Close the dialog after the provider accepts the objective.
 */
export const SetGoalDialog: Component<SetGoalDialogProps> = (props) => {
  const [submitting, setSubmitting] = createSignal(false)
  let active = true
  onCleanup(() => {
    active = false
  })
  /**
   * The editor reports this copy for byte notices only. Submission reads the live document.
   * The Markdown listener uses a trailing 200ms debounce, which further input can delay.
   * The editor reports its initial document directly after construction.
   */
  const [objective, setObjective] = createSignal('')
  /** The reason for the last rejected submission, or undefined when none exists. */
  const [refusal, setRefusal] = createSignal<string | undefined>()
  /** The editor installs its send function before it reports readiness. */
  const [ready, setReady] = createSignal(false)
  // Connect the editor to its instruction through aria-labelledby.
  const hintId = createUniqueId()
  let triggerSend: (() => void | Promise<void>) | undefined

  const trimmed = () => objective().trim()
  // Match the worker's UTF-8 byte limit. The worker rejects oversized objectives without truncation.
  const usedBytes = () => utf8ByteLength(trimmed())
  const showsBudget = () => usedBytes() >= GOAL_OBJECTIVE_BYTE_LIMIT * BUDGET_NOTICE_RATIO

  /** Use the same byte limit and message for the notice and submission validation. */
  const overLimitMessage = (text: string): string | undefined => {
    const used = utf8ByteLength(text)
    if (used <= GOAL_OBJECTIVE_BYTE_LIMIT)
      return undefined
    return `Too long by ${formatNumber(used - GOAL_OBJECTIVE_BYTE_LIMIT)} bytes. `
      + `The limit is ${formatNumber(GOAL_OBJECTIVE_BYTE_LIMIT)}.`
  }

  /** Why the dialog refuses `text`, or `undefined` when it accepts it. */
  const refusalFor = (text: string): string | undefined =>
    text === '' ? 'Write the condition the agent works toward.' : overLimitMessage(text)

  /** Show the size warning during input. Show an empty-input warning only after submission. */
  const notice = () => refusal() ?? overLimitMessage(trimmed())

  /**
   * Validate the live document that the editor supplies. Return false to retain rejected input.
   * The editor handles both its send key and the footer button through this function.
   * The footer uses type="button" so Dialog cannot create a second Enter submission path.
   * Dialog ignores the LinkPopover submit button. The editor applies the user's enterKeyMode preference.
   */
  const submit = async (markdown: string): Promise<boolean | void> => {
    if (submitting())
      return false
    const text = markdown.trim()
    const why = refusalFor(text)
    if (why !== undefined) {
      setRefusal(why)
      return false
    }
    const close = props.onClose
    setSubmitting(true)
    try {
      await props.onSubmit(text)
      // A dialog that the user already dismissed must not close a NEW one: `close` is
      // the handler this render captured, and the owner is gone.
      if (!active)
        return false
      close()
    }
    catch (error) {
      if (active)
        setRefusal(formatErrorMessage(error, 'Could not update the session goal.') || 'Could not update the session goal.')
      return false
    }
    finally {
      if (active)
        setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="Session goal"
      busy={submitting()}
      onClose={props.onClose}
      data-testid="set-goal-dialog"
    >
      {/* Direct section and footer children receive Dialog's scrolling and spacing styles.
          A wrapper form would contain the editor's LinkPopover form, which remains mounted while closed.
          It could also navigate on implicit submission if a suitable input were added without a submit handler. */}
      <section class={styles.field}>
        {/* The contenteditable editor is not a labelable form control.
            aria-labelledby connects this instruction to the editor for screen readers. */}
        <div class={styles.label} id={hintId}>
          The agent keeps working until this condition holds.
        </div>
        <MarkdownEditor
          disabled={submitting()}
          surface="goal"
          ariaLabelledBy={hintId}
          // Supply the initial document during construction.
          // A saved draft could replace the current objective with abandoned text.
          initialMarkdown={props.initialObjective}
          onSend={submit}
          // Let submit explain why it rejects an empty document.
          allowEmptySend
          onMarkdownChange={(markdown) => {
            setObjective(markdown)
            // Clear the previous rejection after the editor reports new text.
            setRefusal(undefined)
          }}
          minHeight={EDITOR_MIN_HEIGHT_PX}
          maxHeight={EDITOR_MAX_HEIGHT_PX}
          placeholder="Describe the condition the agent works toward..."
          imperative={{
            sendRef: (send) => { triggerSend = send },
            // Enable submission after the editor installs its document and send function.
            onReady: () => setReady(true),
          }}
        />
        <Show
          when={notice()}
          fallback={(
            <Show when={showsBudget()}>
              <span class={styles.label} data-testid="set-goal-budget">
                {`${formatNumber(GOAL_OBJECTIVE_BYTE_LIMIT - usedBytes())} bytes left`}
              </span>
            </Show>
          )}
        >
          {/* Announce rejected input to screen readers. */}
          {why => (
            <span class={errorText} role="alert" data-testid="set-goal-refusal">
              {why()}
            </span>
          )}
        </Show>
      </section>
      <footer class={actionsFooter}>
        <button type="button" class="outline" disabled={submitting()} onClick={() => props.onClose()}>Cancel</button>
        {/* Disable submission until the editor is ready and while a request is pending.
            The delayed text copy supplies notices only. Submit validates the live document. */}
        <button
          type="button"
          data-testid="set-goal-submit"
          disabled={!ready() || submitting()}
          onClick={() => void triggerSend?.()}
        >
          Set goal
        </button>
      </footer>
    </Dialog>
  )
}
