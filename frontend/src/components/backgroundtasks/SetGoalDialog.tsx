import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { MarkdownEditor } from '~/components/chat/markdownEditor/MarkdownEditor'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { utf8ByteLength } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './SetGoalDialog.css'

export interface SetGoalDialogProps {
  /**
   * The objective to start from. Set when REPLACING, so the reader edits what
   * is there instead of retyping it, and empty when setting a first goal.
   */
  initialObjective?: string
  onSubmit: (objective: string) => void
  onClose: () => void
}

/**
 * The share of the byte cap at which the remaining budget appears.
 *
 * A counter over a two-line objective states a number nobody is close to
 * spending; a counter over a page of prose is the warning that the refusal is
 * near. 90% is where the second becomes true.
 */
const BUDGET_NOTICE_RATIO = 0.9

/** The editor's opening height and its ceiling, in pixels. */
const EDITOR_MIN_HEIGHT_PX = 120
const EDITOR_MAX_HEIGHT_PX = 320

/**
 * The editor for a session goal's objective.
 *
 * A dialog rather than an input inside the work panel, for two reasons. An
 * objective is PROSE -- Codex accepts 4000 characters -- and the panel's popover
 * variant is capped at 360px wide and 60vh tall, which is a bad box to write a
 * paragraph in. And the panel is a `DropdownMenu as="card"`, whose whole point
 * is that a click inside it does not dismiss it; a form that must close on
 * submit fights that.
 *
 * The field is the app's own markdown editor, so a goal is written with the
 * lists, links and code spans the card renders -- and with the keys the user
 * already learned in the composer.
 */
export const SetGoalDialog: Component<SetGoalDialogProps> = (props) => {
  /**
   * The editor's text, as the EDITOR reports it. Never seeded from the prop.
   *
   * The editor is built asynchronously, so its document is empty for the first
   * frames even when this dialog opens to replace an objective. Seeding this
   * signal from the prop armed the Set goal button during that window, and a
   * click there ran a send against an empty document and did nothing at all.
   * Reading the editor makes the button live at exactly the moment a click
   * works, because the send reads the same document.
   *
   * The seed itself arrives here synchronously: a programmatic `set` reports
   * its text as it applies it. Typing arrives through Milkdown's
   * `markdownUpdated` listener, which is debounced 200ms -- the same lag the
   * composer's own Send button lives with, from the same listener. Enter waits
   * for neither: the editor's send reads the ProseMirror document directly.
   */
  const [objective, setObjective] = createSignal('')
  let triggerSend: (() => void | Promise<void>) | undefined
  let setEditorContent: ((text: string) => void) | undefined

  const trimmed = () => objective().trim()
  // Measured in UTF-8 bytes, the unit the worker's cap counts in. The worker
  // REFUSES an objective over the limit rather than truncating it, so the
  // dialog has to refuse it too or the user meets an error they cannot see the
  // cause of.
  const usedBytes = () => utf8ByteLength(trimmed())
  const overLimit = () => usedBytes() > GOAL_OBJECTIVE_BYTE_LIMIT
  const showsBudget = () => usedBytes() >= GOAL_OBJECTIVE_BYTE_LIMIT * BUDGET_NOTICE_RATIO
  const canSubmit = () => trimmed() !== '' && !overLimit()

  /**
   * The ONE submit path.
   *
   * The editor's own send is what commits, whether the user pressed the key or
   * the footer button, which calls the same send. There is deliberately no
   * `type="submit"` button in this dialog: `Dialog` answers a bare Enter by
   * clicking the first one it finds, and that plus the editor's own Enter
   * handling would be two routes to one action. With none, Enter belongs to the
   * editor, and the user's own `enterKeyMode` preference governs it exactly as
   * it governs the composer.
   *
   * Returns `false` when it refuses, which is how the editor knows to keep what
   * the user wrote.
   */
  const submit = (markdown: string): boolean | void => {
    const text = markdown.trim()
    if (text === '' || utf8ByteLength(text) > GOAL_OBJECTIVE_BYTE_LIMIT)
      return false
    props.onSubmit(text)
    props.onClose()
  }

  return (
    <Dialog title="Session goal" onClose={props.onClose} data-testid="set-goal-dialog">
      {/* `<section>` and `<footer>`, because Dialog's own stylesheet targets
          `> .body > form > section` and `> .body > form > footer`. A form with
          neither loses the footer's spacing and the section's scroller.

          No `onSubmit`: nothing in this form submits it -- see `submit`. */}
      <form class={styles.form}>
        <section class={styles.field}>
          {/* A plain `<div>`, not a `<label>`. The editor is a contenteditable
              region rather than a form control, so a `for` would name nothing
              and a click on the label would focus nothing. */}
          <div class={styles.label}>
            The agent keeps working until this condition holds.
          </div>
          <MarkdownEditor
            surface="goal"
            // No `draftKey`. A persisted draft would resurrect prose the user
            // abandoned the next time they open Replace, in place of the
            // objective the worker actually holds -- and the objective, not the
            // draft, is what Replace exists to edit.
            onSend={submit}
            onMarkdownChange={setObjective}
            requestedHeight={EDITOR_MIN_HEIGHT_PX}
            maxHeight={EDITOR_MAX_HEIGHT_PX}
            placeholder="Describe the condition the agent works toward..."
            imperative={{
              sendRef: (send) => { triggerSend = send },
              contentRef: (_get, set) => { setEditorContent = set },
              // Seeded on ready rather than through a draft key. The editor is
              // built asynchronously, and `onReady` is the moment it holds a
              // document to replace.
              onReady: () => {
                if (props.initialObjective)
                  setEditorContent?.(props.initialObjective)
              },
            }}
          />
          <Show
            when={overLimit()}
            fallback={(
              <Show when={showsBudget()}>
                <span class={styles.label} data-testid="set-goal-budget">
                  {`${(GOAL_OBJECTIVE_BYTE_LIMIT - usedBytes()).toLocaleString()} bytes left`}
                </span>
              </Show>
            )}
          >
            <span class={errorText} data-testid="set-goal-too-long">
              {`Too long by ${(usedBytes() - GOAL_OBJECTIVE_BYTE_LIMIT).toLocaleString()} bytes. The limit is ${GOAL_OBJECTIVE_BYTE_LIMIT.toLocaleString()}.`}
            </span>
          </Show>
        </section>
        <footer class={actionsFooter}>
          <button type="button" class="outline" onClick={() => props.onClose()}>Cancel</button>
          <button
            type="button"
            data-testid="set-goal-submit"
            disabled={!canSubmit()}
            onClick={() => void triggerSend?.()}
          >
            Set goal
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
