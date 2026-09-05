import type { Component } from 'solid-js'
import { createSignal } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
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

  const submit = (e: Event) => {
    e.preventDefault()
    if (trimmed() === '')
      return
    props.onSubmit(trimmed())
    props.onClose()
  }

  return (
    <Dialog title="Session goal" onClose={props.onClose} data-testid="set-goal-dialog">
      <form class={styles.form} onSubmit={submit}>
        <label class={styles.label} for="goal-objective-input">
          The agent keeps working until this condition holds.
        </label>
        <textarea
          id="goal-objective-input"
          class={styles.input}
          data-testid="set-goal-input"
          value={objective()}
          onInput={e => setObjective(e.currentTarget.value)}
          rows={4}
          // Autofocus is safe HERE and not in the panel: a dialog already took
          // focus from the page, so claiming it inside costs nothing -- whereas
          // the panel's popover opens beside a composer the user may be typing
          // in.
          autofocus
        />
        <div class={styles.actions}>
          <button type="button" class={styles.button} onClick={() => props.onClose()}>Cancel</button>
          <button
            type="submit"
            class={styles.button}
            data-testid="set-goal-submit"
            disabled={trimmed() === ''}
          >
            Set goal
          </button>
        </div>
      </form>
    </Dialog>
  )
}
