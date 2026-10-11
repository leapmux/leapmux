import type { Component } from 'solid-js'
import type { TodoItem } from '~/models/todo'
import { Match, Switch } from 'solid-js'
import * as styles from './TaskCheckbox.css'

export type TaskCheckboxStatus = TodoItem['status']

interface TaskCheckboxProps {
  status: TaskCheckboxStatus
}

// Inset by half the stroke width so the outer edge sits flush with the
// SVG boundary. Every outlined state uses this rectangle.
const INSET_RECT = { x: '0.75', y: '0.75', width: '22.5', height: '22.5', rx: '3' } as const
// Fill the whole square for completed and deleted tasks.
const FULL_RECT = { x: '0', y: '0', width: '24', height: '24', rx: '3' } as const

const STATUS_LABELS = {
  pending: 'Pending',
  in_progress: 'In progress',
  completed: 'Completed',
  deleted: 'Deleted',
  blocked: 'Blocked',
} satisfies Record<TaskCheckboxStatus, string>

export const TaskCheckbox: Component<TaskCheckboxProps> = (props) => {
  return (
    <svg
      class={styles.svg}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      data-task-checkbox={props.status}
      role="img"
      aria-label={STATUS_LABELS[props.status]}
    >
      <Switch>
        <Match when={props.status === 'blocked'}>
          <rect class={styles.boxPending} {...INSET_RECT} />
          <path class={`${styles.glyph} ${styles.glyphBlocked}`} d="M6 12 H18" />
        </Match>
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
