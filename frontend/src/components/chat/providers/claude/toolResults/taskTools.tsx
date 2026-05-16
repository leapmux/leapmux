import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { For, Show } from 'solid-js'
import { pickString, pickStringArray } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { TodoListMessage } from '../../../todoListMessage'
import { claudeTaskListToTodos, claudeTaskToTodoItem } from '../extractors/todo'
import * as styles from './taskTools.css'

/**
 * Result-side view for Claude TaskList. The full authoritative task
 * array renders as a TodoListMessage so it matches the sidebar visuals.
 */
export function TaskListResultView(props: {
  tasks: unknown[]
  context?: RenderContext
}): JSX.Element {
  const todos = () => claudeTaskListToTodos(props.tasks)
  return (
    <TodoListMessage
      source={{
        toolName: CLAUDE_TOOL.TASK_LIST,
        title: pluralize(todos().length, 'task'),
        todos: todos(),
      }}
      context={props.context}
    />
  )
}

/**
 * Result-side view for Claude TaskGet. A single-row card plus secondary
 * lines for `description`, `blockedBy`, and `blocks` (only the
 * non-empty ones).
 */
export function TaskGetResultView(props: {
  task: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const todos = () => {
    const item = claudeTaskToTodoItem(props.task, { includeDescription: true })
    return item ? [item] : []
  }
  const description = () => pickString(props.task, 'description')
  const blockedBy = () => pickStringArray(props.task, 'blockedBy')
  const blocks = () => pickStringArray(props.task, 'blocks')
  const taskId = () => todos()[0]?.id ?? ''

  return (
    <Show when={taskId()}>
      <TodoListMessage
        source={{
          toolName: CLAUDE_TOOL.TASK_GET,
          title: `Task #${taskId()}`,
          todos: todos(),
        }}
        context={props.context}
      />
      <Show when={description() || blockedBy().length > 0 || blocks().length > 0}>
        <ul class={styles.detailList}>
          <Show when={description()}>
            <li class={styles.detailLine}>{description()}</li>
          </Show>
          <IdListLine label="Blocked by" ids={blockedBy()} />
          <IdListLine label="Blocks" ids={blocks()} />
        </ul>
      </Show>
    </Show>
  )
}

/** Renders one detail line listing referenced task IDs (e.g. "Blocked by: #3 #7"). */
function IdListLine(props: { label: string, ids: readonly string[] }): JSX.Element {
  return (
    <Show when={props.ids.length > 0}>
      <li class={styles.detailLine}>
        {`${props.label}: `}
        <For each={props.ids}>
          {id => (
            <span class={styles.idRef}>
              #
              {id}
            </span>
          )}
        </For>
      </li>
    </Show>
  )
}
