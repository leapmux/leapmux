import type { Component } from 'solid-js'
import type { TodoItem } from '~/stores/chatTodos'
import { Match, Switch } from 'solid-js'
import * as styles from './TaskCheckbox.css'

export type TaskCheckboxStatus = TodoItem['status']

interface TaskCheckboxProps {
  status: TaskCheckboxStatus
}

// Inset by half the stroke width so the outer edge sits flush with the
// SVG boundary. Used for outlined states (pending, in_progress).
const INSET_RECT = { x: '0.75', y: '0.75', width: '22.5', height: '22.5', rx: '3' } as const
// Full-bleed fill for terminal states (completed, deleted).
const FULL_RECT = { x: '0', y: '0', width: '24', height: '24', rx: '3' } as const

export const TaskCheckbox: Component<TaskCheckboxProps> = (props) => {
  return (
    <svg
      class={styles.svg}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      data-task-checkbox={props.status}
      aria-hidden="true"
    >
      <Switch>
        <Match when={props.status === 'pending'}>
          <rect class={styles.boxPending} {...INSET_RECT} />
        </Match>
        <Match when={props.status === 'completed'}>
          <rect class={styles.boxCompleted} {...FULL_RECT} />
          <polyline class={`${styles.glyph} ${styles.glyphCompleted}`} points="20 6 9 17 4 12" />
        </Match>
        <Match when={props.status === 'deleted'}>
          <rect class={styles.boxDeleted} {...FULL_RECT} />
          <path class={`${styles.glyph} ${styles.glyphDeleted}`} d="M6 6 L18 18 M18 6 L6 18" />
        </Match>
        <Match when={props.status === 'in_progress'}>
          <rect class={styles.antsRect} {...INSET_RECT} />
        </Match>
      </Switch>
    </svg>
  )
}
