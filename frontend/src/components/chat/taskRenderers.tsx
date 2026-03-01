/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { TodoItem } from '~/stores/chat.store'
import ListTodo from 'lucide-solid/icons/list-todo'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Terminal from 'lucide-solid/icons/terminal'
import Vote from 'lucide-solid/icons/vote'
import { For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { TodoList } from '~/components/todo/TodoList'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject } from './messageUtils'
import { firstNonEmptyLine, formatTaskStatus } from './rendererUtils'
import { ControlResponseTag, ToolHeaderActions } from './toolRenderers'
import {
  answerText,
  toolInputDetail,
  toolInputSubDetail,
  toolInputSubDetailExpanded,
  toolMessage,
  toolResultContentAnsi,
  toolResultContentPre,
  toolUseHeader,
  toolUseIcon,
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
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="TodoWrite">
          <Icon icon={ListTodo} size="md" class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>{label}</span>
        <ControlResponseTag response={context?.childControlResponse} />
        <Show when={context}>
          <ToolHeaderActions
            createdAt={context!.createdAt}
            updatedAt={context!.updatedAt}
            threadCount={context!.threadChildCount ?? 0}
            threadExpanded={context!.threadExpanded ?? false}
            onToggleThread={context!.onToggleThread ?? (() => {})}
            onCopyJson={context!.onCopyJson ?? (() => {})}
            jsonCopied={context!.jsonCopied ?? false}
          />
        </Show>
      </div>
      <TodoList todos={todos} />
    </div>
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
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="AskUserQuestion">
          <Icon icon={Vote} size="md" class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>{statusText}</span>
        <ControlResponseTag response={context?.childControlResponse} />
        <Show when={context}>
          <ToolHeaderActions
            createdAt={context!.createdAt}
            updatedAt={context!.updatedAt}
            threadCount={context!.threadChildCount ?? 0}
            threadExpanded={context!.threadExpanded ?? false}
            onToggleThread={context!.onToggleThread ?? (() => {})}
            onCopyJson={context!.onCopyJson ?? (() => {})}
            jsonCopied={context!.jsonCopied ?? false}
          />
        </Show>
      </div>
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
    </div>
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
  const expanded = context?.threadExpanded ?? false

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="TaskOutput">
          <Icon icon={SquareTerminal} size="md" class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>
          {status}
          {description ? ` - ${description}` : ''}
        </span>
        <ControlResponseTag response={context?.childControlResponse} />
        <Show when={context}>
          <ToolHeaderActions
            createdAt={context!.createdAt}
            updatedAt={context!.updatedAt}
            threadCount={context!.threadChildCount ?? 0}
            threadExpanded={context!.threadExpanded ?? false}
            onToggleThread={context!.onToggleThread ?? (() => {})}
            onCopyJson={context!.onCopyJson ?? (() => {})}
            jsonCopied={context!.jsonCopied ?? false}
          />
        </Show>
      </div>
      <Show when={!expanded && firstLine}>
        <div class={toolInputSubDetail}>{firstLine}</div>
      </Show>
      <Show when={expanded}>
        <div class={toolInputSubDetailExpanded}>
          <Show when={task?.task_id}>
            {`task_id: ${task!.task_id}`}
          </Show>
          <Show when={task?.task_type}>
            {`\ntask_type: ${task!.task_type}`}
          </Show>
          <Show when={task?.status}>
            {`\nstatus: ${task!.status}`}
          </Show>
          <Show when={description}>
            {`\ndescription: ${description}`}
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
      </Show>
    </div>
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

/** Renders task_notification system messages as a tool-use-style block with Terminal icon. */
export const taskNotificationRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_notification')
      return null

    const summary = typeof parsed.summary === 'string' ? parsed.summary : 'Task notification'
    const outputFile = typeof parsed.output_file === 'string' ? parsed.output_file : null

    return (
      <div class={toolMessage}>
        <div class={toolUseHeader}>
          <span class={inlineFlex} title="Task Notification">
            <Icon icon={Terminal} size="md" class={toolUseIcon} />
          </span>
          <span class={toolInputDetail}>{summary}</span>
          <Show when={context}>
            <ToolHeaderActions
              threadCount={context!.threadChildCount ?? 0}
              threadExpanded={context!.threadExpanded ?? false}
              onToggleThread={context!.onToggleThread ?? (() => {})}
              onCopyJson={context!.onCopyJson ?? (() => {})}
              jsonCopied={context!.jsonCopied ?? false}
            />
          </Show>
        </div>
        <Show when={outputFile}>
          <div class={toolInputSubDetail}>{outputFile}</div>
        </Show>
      </div>
    )
  },
}
