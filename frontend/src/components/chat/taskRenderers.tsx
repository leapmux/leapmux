/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chat.store'
import Check from 'lucide-solid/icons/check'
import ListTodo from 'lucide-solid/icons/list-todo'
import Vote from 'lucide-solid/icons/vote'
import { For, Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { useCopyButton } from '~/hooks/useCopyButton'
import { todosToMarkdown } from '~/lib/messageParser'
import { getAssistantContent, isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  toolInputSummary,
} from './toolStyles.css'

/** Render TodoWrite tool_use with a visual todo list. Returns null if input is invalid. */
export function renderTodoWrite(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = toolUse.input
  if (!isObject(input) || !Array.isArray((input as Record<string, unknown>).todos))
    return null

  const todos: TodoItem[] = ((input as Record<string, unknown>).todos as Array<Record<string, unknown>>).map(t => ({
    content: String(t.content || ''),
    status: (t.status === 'in_progress' ? 'in_progress' : t.status === 'completed' ? 'completed' : 'pending') as TodoItem['status'],
    activeForm: String(t.activeForm || ''),
  }))

  const count = todos.length
  const label = `${count} task${count === 1 ? '' : 's'}`

  const md = todosToMarkdown(todos)
  const { copied, copy } = useCopyButton(() => md)
  const reply = context?.onReply ? () => context.onReply!(md) : undefined

  return (
    <ToolUseLayout
      icon={ListTodo}
      toolName="TodoWrite"
      title={label}
      alwaysVisible={true}
      context={context}
      onReply={reply}
      onCopyMarkdown={copy}
      markdownCopied={copied()}
    >
      <TodoList todos={todos} />
    </ToolUseLayout>
  )
}

/** Render AskUserQuestion tool_use with questions and options. Returns null if input is invalid. */
export function renderAskUserQuestion(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = toolUse.input
  if (!isObject(input))
    return null

  const questions = (input as Record<string, unknown>).questions as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(questions) || questions.length === 0)
    return null

  const title = questions.length === 1
    ? String(questions[0].question || questions[0].header || 'Question')
    : `${questions.length} questions`

  return (
    <ToolUseLayout
      icon={Vote}
      toolName="AskUserQuestion"
      title={title}
      alwaysVisible={true}
      context={context}
    >
      <For each={questions}>
        {(q) => {
          const header = String(q.header || '')
          const options = Array.isArray(q.options) ? q.options as Array<Record<string, unknown>> : []
          return (
            <div style={{ 'margin-top': '4px' }}>
              <Show when={questions.length > 1}>
                <div><strong>{header}</strong></div>
              </Show>
              <For each={options}>
                {opt => <div class={toolInputSummary}>{String(opt.label || '')}</div>}
              </For>
            </div>
          )
        }}
      </For>
    </ToolUseLayout>
  )
}

export const todoWriteRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'TodoWrite')
    if (!toolUse)
      return null
    return renderTodoWrite(toolUse as Record<string, unknown>, context)
  },
}

export const askUserQuestionRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'AskUserQuestion')
    if (!toolUse)
      return null
    return renderAskUserQuestion(toolUse as Record<string, unknown>, context)
  },
}

/** Renders task_notification system messages as a tool-use-style block with Check icon. */
export const taskNotificationRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_notification')
      return null

    const status = typeof parsed.status === 'string' ? parsed.status : 'completed'
    const statusLabel = status.charAt(0).toUpperCase() + status.slice(1)
    const summaryText = typeof parsed.summary === 'string' ? parsed.summary : 'Task notification'
    const title = `${statusLabel}: ${summaryText}`
    const outputFile = typeof parsed.output_file === 'string' ? parsed.output_file : null
    const summary = outputFile ? <div class={toolInputSummary}>{outputFile}</div> : undefined

    return (
      <ToolUseLayout
        icon={Check}
        toolName="Task Notification"
        title={title}
        summary={summary}
        context={context}
      />
    )
  },
}
