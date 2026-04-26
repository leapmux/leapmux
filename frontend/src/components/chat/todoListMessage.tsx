import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chat.store'
import ListTodo from 'lucide-solid/icons/list-todo'
import { Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { useCopyButton } from '~/hooks/useCopyButton'
import { todosToMarkdown } from '~/lib/messageParser'
import { EmptyTodoLayout, ToolUseLayout } from './toolRenderers'

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
  /** Whether the body section gets a left border. Default: true. */
  bordered?: boolean
}

/**
 * Renders a todo-list shaped tool message: header + checklist body, with
 * Reply/Copy-Markdown buttons wired to a markdown-formatted version of the
 * todos. Empty todo list collapses to the shared "cleared" placeholder.
 */
export function TodoListMessage(props: {
  source: TodoListSource
  context?: RenderContext
}): JSX.Element {
  const todos = () => props.source.todos
  const md = () => todosToMarkdown(todos())
  const { copied, copy } = useCopyButton(() => md())
  const onReplyClick = () => props.context?.onReply?.(md())
  const reply = () => props.context?.onReply ? onReplyClick : undefined

  return (
    <Show
      when={todos().length > 0}
      fallback={<EmptyTodoLayout toolName={props.source.toolName} context={props.context} />}
    >
      <ToolUseLayout
        icon={ListTodo}
        toolName={props.source.toolName}
        title={props.source.title}
        alwaysVisible={true}
        bordered={props.source.bordered}
        context={props.context}
        headerActions={{
          onReply: reply(),
          onCopyMarkdown: copy,
          markdownCopied: copied(),
        }}
      >
        <TodoList todos={todos()} />
      </ToolUseLayout>
    </Show>
  )
}
