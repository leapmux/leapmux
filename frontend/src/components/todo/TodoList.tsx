import type { Component } from 'solid-js'
import type { TodoItem } from '~/stores/chat.store'
import Check from 'lucide-solid/icons/check'
import Circle from 'lucide-solid/icons/circle'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { For, Match, Switch } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './TodoList.css'

interface TodoListProps {
  todos: TodoItem[]
}

export const TodoList: Component<TodoListProps> = (props) => {
  return (
    <div class={styles.todoList}>
      <For each={props.todos}>
        {todo => (
          <div class={`${styles.todoItem} ${todo.status === 'completed' ? styles.todoCompleted : ''} ${todo.status === 'in_progress' ? styles.todoInProgress : ''}`}>
            <div class={styles.todoIcon}>
              <Switch>
                <Match when={todo.status === 'completed'}>
                  <Icon icon={Check} size="sm" class={styles.checkIcon} />
                </Match>
                <Match when={todo.status === 'in_progress'}>
                  <Icon icon={LoaderCircle} size="sm" class={`${styles.spinnerIcon} ${spinner}`} />
                </Match>
                <Match when={todo.status === 'pending'}>
                  <Icon icon={Circle} size="sm" class={styles.pendingIcon} />
                </Match>
              </Switch>
            </div>
            <span class={styles.todoText}>
              {todo.status === 'in_progress' ? todo.activeForm : todo.content}
            </span>
          </div>
        )}
      </For>
    </div>
  )
}
