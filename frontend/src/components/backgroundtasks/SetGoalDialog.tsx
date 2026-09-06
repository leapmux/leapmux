import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { utf8ByteLength } from '~/lib/validate'
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
 * The editor for a session goal's objective.
 *
 * A dialog rather than an input inside the work panel, for two reasons. An
 * objective is PROSE -- Codex accepts 4000 characters -- and the panel's popover
 * variant is capped at 360px wide and 60vh tall, which is a bad box to write a
 * paragraph in. And the panel is a `DropdownMenu as="card"`, whose whole point
 * is that a click inside it does not dismiss it; a form that must close on
 * submit fights that.
 */
export const SetGoalDialog: Component<SetGoalDialogProps> = (props) => {
  const [objective, setObjective] = createSignal(props.initialObjective ?? '')
  const trimmed = () => objective().trim()
  // Measured in UTF-8 bytes, the unit the worker's cap counts in. The worker
  // REFUSES an objective over the limit rather than truncating it, so the
  // dialog has to refuse it too or the user meets an error they cannot see the
  // cause of.
  const overLimit = () => utf8ByteLength(trimmed()) > GOAL_OBJECTIVE_BYTE_LIMIT

  const submit = (e: Event) => {
    e.preventDefault()
    if (trimmed() === '' || overLimit())
      return
    props.onSubmit(trimmed())
    props.onClose()
  }

  return (
    <Dialog title="Session goal" onClose={props.onClose} data-testid="set-goal-dialog">
      {/* `<section>` and `<footer>`, because Dialog's own stylesheet targets
          `> .body > form > section` and `> .body > form > footer`. A form with
          neither loses the footer's spacing and the section's scroller. */}
      <form class={styles.form} onSubmit={submit}>
        <section class={styles.field}>
          <label class={styles.label} for="goal-objective-input">
            The agent keeps working until this condition holds.
          </label>
          <Show when={overLimit()}>
            <span class={styles.label} data-testid="set-goal-too-long">
              {`Too long by ${utf8ByteLength(trimmed()) - GOAL_OBJECTIVE_BYTE_LIMIT} bytes. The limit is ${GOAL_OBJECTIVE_BYTE_LIMIT}.`}
            </span>
          </Show>
          <textarea
            id="goal-objective-input"
            class={styles.input}
            data-testid="set-goal-input"
            value={objective()}
            onInput={e => setObjective(e.currentTarget.value)}
            rows={4}
            // No `maxlength`. It counts UTF-16 code units and the worker's cap
            // counts UTF-8 BYTES, so it would let 2000 CJK characters through
            // (about 6000 bytes) and the worker would refuse them -- a limit
            // that reads as satisfied while the submit button says otherwise.
            // The byte count below is the one the worker enforces.
            // Autofocus is safe HERE and not in the panel: a dialog already took
            // focus from the page, so claiming it inside costs nothing -- whereas
            // the panel's popover opens beside a composer the user may be typing
            // in.
            autofocus
          />
        </section>
        <footer class={actionsFooter}>
          <button type="button" class="outline" onClick={() => props.onClose()}>Cancel</button>
          <button
            type="submit"
            data-testid="set-goal-submit"
            disabled={trimmed() === '' || overLimit()}
          >
            Set goal
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
