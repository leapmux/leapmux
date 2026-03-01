/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chat.store'
import ListTodo from 'lucide-solid/icons/list-todo'
import Terminal from 'lucide-solid/icons/terminal'
import Vote from 'lucide-solid/icons/vote'
import { For, Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  answerText,
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

  return (
    <ToolUseLayout
      icon={ListTodo}
      toolName="TodoWrite"
      title={label}
      alwaysVisible={true}
      context={context}
    >
      <TodoList todos={todos} />
    </ToolUseLayout>
  )
}

/** Render AskUserQuestion tool_use with questions and inline answers. Returns null if input is invalid. */
export function renderAskUserQuestion(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = toolUse.input
  if (!isObject(input))
    return null

  const questions = (input as Record<string, unknown>).questions as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(questions) || questions.length === 0)
    return null

  const answers = context?.childAnswers
  const hasAnswers = !!answers && Object.keys(answers).length > 0
  const hasChild = (context?.threadChildCount ?? 0) > 0
  const statusText = hasAnswers
    ? 'Submitted answers'
    : (hasChild && context?.childResultContent)
        ? context.childResultContent
        : 'Waiting for answers'

  return (
    <ToolUseLayout
      icon={Vote}
      toolName="AskUserQuestion"
      title={statusText}
      alwaysVisible={true}
      context={context}
    >
      <ul style={{ 'padding-left': '20px', 'margin': '4px 0 0' }}>
        <For each={questions}>
          {(q) => {
            const header = String(q.header || '')
            const answer = answers?.[header]
            return (
              <li>
                <strong>{`${header}: `}</strong>
                <Show when={answer} fallback={<em>Not answered</em>}>
                  <div class={`${answerText} ${markdownContent}`} innerHTML={renderMarkdown(answer!)} />
                </Show>
              </li>
            )
          }}
        </For>
      </ul>
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

/** Renders task_notification system messages as a tool-use-style block with Terminal icon. */
export const taskNotificationRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_notification')
      return null

    const summaryText = typeof parsed.summary === 'string' ? parsed.summary : 'Task notification'
    const outputFile = typeof parsed.output_file === 'string' ? parsed.output_file : null
    const summary = outputFile ? <div class={toolInputSummary}>{outputFile}</div> : undefined

    return (
      <ToolUseLayout
        icon={Terminal}
        toolName="Task Notification"
        title={summaryText}
        summary={summary}
        context={context}
      />
    )
  },
}
