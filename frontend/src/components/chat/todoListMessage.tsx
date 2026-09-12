import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chatTodos'
import ListTodo from 'lucide-solid/icons/list-todo'
import { Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { useCopyButton } from '~/hooks/useCopyButton'
import { todosToMarkdown } from '~/lib/messageParser'
import { toolInputSummary } from './toolStyles.css'
import { ToolMessageLayout } from './widgets/ToolMessageLayout'

/**
 * Provider-neutral source for todo-list-style tool messages
 * (TodoWrite, Plan, Plan Update). Empty `todos` triggers the
 * "To-do list cleared" empty state.
 */
export interface TodoListSource {
  /** Tool name shown on the icon tooltip (e.g. "TodoWrite", "Plan", "Plan Update"). */
  toolName: string
  /** Header title (e.g. "5 tasks", "Plan", "Plan Update — fix login bug"). */
  title: string
  todos: TodoItem[]
  emptyText?: string
  /** Whether the body section gets a left border. Default: true. */
  bordered?: boolean
}

/** Render the checklist or its explicit empty state. */
export function TodoListBody(props: { todos: TodoItem[], emptyText?: string }): JSX.Element {
  return <Show when={props.todos.length > 0} fallback={<div class={toolInputSummary}>{props.emptyText ?? 'To-do list cleared'}</div>}><TodoList todos={props.todos} variant="full" /></Show>
}

/**
 * Renders a todo-list shaped tool message: header + checklist body, with
 * Reply/Copy-Markdown buttons wired to a markdown-formatted version of the
 * todos. Empty todo list collapses to the shared "cleared" placeholder.
 */
export function TodoListMessage(props: {
  source: TodoListSource
  context?: RenderContext
  role?: 'request' | 'result'
  hasRequest?: boolean
  showBody?: boolean
}): JSX.Element {
  const todos = () => props.source.todos
  const md = () => todosToMarkdown(todos())
  const { copied, copy } = useCopyButton(() => md())
  const onReplyClick = () => props.context?.onReply?.(md())
  const reply = () => props.context?.onReply ? onReplyClick : undefined

  return (
    <ToolMessageLayout
      role={props.role ?? 'request'}
      hasRequest={props.hasRequest}
      icon={ListTodo}
      toolName={props.source.toolName}
      title={todos().length > 0 ? props.source.title : 'To-do list'}
      alwaysVisible={true}
      bordered={props.source.bordered}
      context={props.context}
      headerActions={{
        onReply: reply(),
        onCopyMarkdown: copy,
        markdownCopied: copied(),
      }}
    >
      <Show when={props.showBody !== false}><TodoListBody todos={todos()} emptyText={props.source.emptyText} /></Show>
    </ToolMessageLayout>
  )
}
