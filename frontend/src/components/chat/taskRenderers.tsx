/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chat.store'
import Bot from 'lucide-solid/icons/bot'
import ListTodo from 'lucide-solid/icons/list-todo'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Terminal from 'lucide-solid/icons/terminal'
import Vote from 'lucide-solid/icons/vote'
import { For, Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject } from './messageUtils'
import { firstNonEmptyLine, formatDuration, formatNumber, formatTaskStatus } from './rendererUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  answerText,
  toolInputSummary,
  toolInputSummaryExpanded,
  toolResultContentAnsi,
  toolResultContentPre,
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
  const completedCount = todos.filter(t => t.status === 'completed').length
  const inProgressTask = todos.find(t => t.status === 'in_progress')

  const summary = (
    <div class={toolInputSummary}>
      {inProgressTask ? `${inProgressTask.activeForm} \u00B7 ` : ''}
      {`${completedCount}/${count} completed`}
    </div>
  )

  return (
    <ToolUseLayout
      icon={ListTodo}
      toolName="TodoWrite"
      title={label}
      summary={summary}
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

/** Render TaskOutput tool_use with task status, description, and output. */
export function renderTaskOutput(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  void toolUse
  const task = context?.childTask
  const status = formatTaskStatus(task?.status)
  const description = task?.description
  const output = task?.output
  const firstLine = firstNonEmptyLine(output)

  const title = `${status}${description ? ` - ${description}` : ''}`
  const summary = firstLine ? <div class={toolInputSummary}>{firstLine}</div> : undefined

  return (
    <ToolUseLayout
      icon={SquareTerminal}
      toolName="TaskOutput"
      title={title}
      summary={summary}
      context={context}
    >
      <>
        <div class={toolInputSummaryExpanded}>
          <Show when={task?.task_id}>
            {`task_id: ${task!.task_id}`}
          </Show>
          <Show when={task?.task_type}>
            {`\ntask_type: ${task!.task_type}`}
          </Show>
          <Show when={task?.exitCode !== undefined}>
            {`\nexitCode: ${task!.exitCode}`}
          </Show>
        </div>
        <Show when={output}>
          {containsAnsi(output!)
            ? <div class={toolResultContentAnsi} innerHTML={renderAnsi(output!)} />
            : <div class={toolResultContentPre}>{output}</div>}
        </Show>
      </>
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

export const taskOutputRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'TaskOutput')
    if (!toolUse)
      return null
    return renderTaskOutput(toolUse as Record<string, unknown>, context)
  },
}

/** Render Agent or Task tool_use with description, status, and subagent type. */
export function renderAgentOrTask(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = isObject(toolUse.input) ? toolUse.input as Record<string, unknown> : {}
  const toolName = String(toolUse.name || 'Agent')
  const description = String(input.description || toolName)
  const subagentType = input.subagent_type ? String(input.subagent_type) : null

  // Format title: if description starts with subagent name, use "SubAgent: rest" format
  let titleDesc = description
  if (subagentType) {
    const prefix = subagentType.toLowerCase()
    const descLower = description.toLowerCase()
    if (descLower.startsWith(`${prefix} `))
      titleDesc = `${subagentType}: ${description.slice(subagentType.length + 1)}`
  }

  const status = context?.childToolResultStatus
  const hasChildren = (context?.threadChildCount ?? 0) > 0
  const displayStatus = status
    ? formatTaskStatus(status)
    : (hasChildren ? 'Running' : null)

  const title = `${titleDesc}${displayStatus ? ` - ${displayStatus}` : ''}${subagentType ? ` (${subagentType})` : ''}`

  // Stats summary from child tool_use_result
  const duration = context?.childTotalDurationMs
  const tokens = context?.childTotalTokens
  const toolUses = context?.childTotalToolUseCount
  const parts: string[] = []
  if (duration !== undefined)
    parts.push(formatDuration(duration))
  if (tokens !== undefined)
    parts.push(`${formatNumber(tokens)} tokens`)
  if (toolUses !== undefined)
    parts.push(`${toolUses} tool use${toolUses === 1 ? '' : 's'}`)
  const summary = parts.length > 0
    ? <div class={toolInputSummary}>{parts.join(' \u00B7 ')}</div>
    : undefined

  return (
    <ToolUseLayout
      icon={Bot}
      toolName={toolName}
      title={title}
      summary={summary}
      context={context}
    />
  )
}

export const agentOrTaskRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && (c.name === 'Agent' || c.name === 'Task'))
    if (!toolUse)
      return null
    return renderAgentOrTask(toolUse as Record<string, unknown>, context)
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
