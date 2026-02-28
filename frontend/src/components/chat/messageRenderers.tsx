/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from './diffUtils'
import type { MessageCategory } from './messageClassification'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { TodoItem } from '~/stores/chat.store'
import Bot from 'lucide-solid/icons/bot'
import Brain from 'lucide-solid/icons/brain'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import Hand from 'lucide-solid/icons/hand'
import ListTodo from 'lucide-solid/icons/list-todo'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Stamp from 'lucide-solid/icons/stamp'
import Terminal from 'lucide-solid/icons/terminal'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import Toolbox from 'lucide-solid/icons/toolbox'
import Vote from 'lucide-solid/icons/vote'
import { createSignal, For, Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { thinkingContent, thinkingHeader } from './messageStyles.css'
import { getAssistantContent, isObject, relativizePath } from './messageUtils'
import {
  agentRenamedRenderer,
  compactBoundaryRenderer,
  contextClearedRenderer,
  controlResponseRenderer,
  interruptedRenderer,
  microcompactBoundaryRenderer,
  rateLimitRenderer,
  resultRenderer,
  settingsChangedRenderer,
  systemInitRenderer,
} from './notificationRenderers'
import {
  ControlResponseTag,
  ToolHeaderActions,
  toolResultRenderer,
  toolUseRenderer,
} from './toolRenderers'
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

export { ToolHeaderActions }

/** Context passed to renderers from MessageBubble. */
export interface RenderContext {
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  /** ISO timestamp of the last update (thread merge). Preferred over createdAt when set. */
  updatedAt?: string
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** Number of thread children (tool results). */
  threadChildCount?: number
  /** Whether thread is currently expanded. */
  threadExpanded?: boolean
  /** Toggle thread expansion. */
  onToggleThread?: () => void
  /** User's preferred diff view. */
  diffView?: DiffViewPreference
  /** Copy raw JSON to clipboard. */
  onCopyJson?: () => void
  /** Whether JSON was just copied (for feedback). */
  jsonCopied?: boolean
  /** Parent tool_use name (passed to child tool_result renderers). */
  parentToolName?: string
  /** Parent tool_use input (passed to child tool_result renderers). */
  parentToolInput?: Record<string, unknown>
  /** structuredPatch from child tool_result (passed to parent tool_use for Edit/Write diffs). */
  childStructuredPatch?: StructuredPatchHunk[]
  /** File path from child tool_result (passed to parent tool_use for Edit/Write diffs). */
  childFilePath?: string
  /** Answers map from child tool_result (header → answer string, for AskUserQuestion). */
  childAnswers?: Record<string, string>
  /** Text content from child tool_result message (for fallback descriptions, e.g. "User stopped"). */
  childResultContent?: string
  /** Whether the child tool_result has is_error=true (for fallback rejection detection). */
  childResultIsError?: boolean
  /** Task data from child tool_result (for TaskOutput renderer). */
  childTask?: {
    task_id?: string
    task_type?: string
    status?: string
    description?: string
    output?: string
    exitCode?: number
  }
  /** Control response (approval/rejection) threaded into this tool_use. */
  childControlResponse?: { action: string, comment: string }
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null
}

function markdownClass(_role: MessageRole): string {
  return markdownContent
}

// ---------------------------------------------------------------------------
// Specialized tool render functions (accept pre-extracted tool_use data)
// ---------------------------------------------------------------------------

/** Render EnterPlanMode tool_use as a simple "Entering Plan Mode" text line. */
function renderEnterPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  void toolUse
  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="EnterPlanMode">
          <TicketsPlane size={16} class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>Entering Plan Mode</span>
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
    </div>
  )
}

/** Render Skill tool_use as "Skill: /<skill name>". */
function renderSkill(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const skillName = isObject(input) ? String((input as Record<string, unknown>).skill || '') : ''

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="Skill">
          <Toolbox size={16} class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>{`Skill: /${skillName}`}</span>
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
    </div>
  )
}

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''

  // Derive effective control response: prefer the explicit controlResponse
  // threaded by the backend; fall back to tool_result-based detection for
  // data where the controlResponse was lost to the pre-fix race condition.
  const effectiveCr = (): { action: string, comment: string } | undefined => {
    if (context?.childControlResponse)
      return context.childControlResponse
    const resultContent = context?.childResultContent
    if (!resultContent)
      return undefined
    if (context?.childResultIsError === true)
      return { action: 'rejected', comment: resultContent }
    if (context?.childResultIsError === false || resultContent.toLowerCase().includes('approved your plan'))
      return { action: 'approved', comment: '' }
    return { action: 'rejected', comment: resultContent }
  }

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title="ExitPlanMode">
          <PlaneTakeoff size={16} class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>Leaving Plan Mode</span>
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
      <Show when={context?.childFilePath}>
        <div class={toolInputSubDetail}>
          {relativizePath(context!.childFilePath!, context?.workingDir, context?.homeDir)}
        </div>
      </Show>
      <Show when={planText}>
        <hr />
        <div class={markdownContent} innerHTML={renderMarkdown(planText)} />
      </Show>
      <Show when={effectiveCr()}>
        {cr => (
          <>
            <hr />
            <div class={toolUseHeader}>
              <span class={inlineFlex}>
                {cr().action === 'approved'
                  ? <Stamp size={16} class={toolUseIcon} />
                  : <Hand size={16} class={toolUseIcon} />}
              </span>
              <span class={toolInputDetail}>
                {cr().action === 'approved' ? 'Approved' : cr().comment ? 'Sent feedback' : 'Rejected'}
              </span>
            </div>
            <Show when={cr().action !== 'approved' && cr().comment}>
              <div class={markdownContent} innerHTML={renderMarkdown(cr().comment)} />
            </Show>
          </>
        )}
      </Show>
    </div>
  )
}

/** Render TodoWrite tool_use with a visual todo list. Returns null if input is invalid. */
function renderTodoWrite(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
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
          <ListTodo size={16} class={toolUseIcon} />
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
function renderAskUserQuestion(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
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
          <Vote size={16} class={toolUseIcon} />
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

/** Format task status for display. */
export function formatTaskStatus(status?: string): string {
  if (!status)
    return 'Pending'
  if (status === 'completed')
    return 'Complete'
  if (status === 'failed')
    return 'Failed'
  return status.charAt(0).toUpperCase() + status.slice(1)
}

/** Return the first non-empty trimmed line from text, or null. */
export function firstNonEmptyLine(text?: string): string | null {
  if (!text)
    return null
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (trimmed)
      return trimmed
  }
  return null
}

/** Render TaskOutput tool_use with task status, description, and output. */
function renderTaskOutput(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
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
          <SquareTerminal size={16} class={toolUseIcon} />
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

// ---------------------------------------------------------------------------
// MessageContentRenderer wrappers (used by getFallbackRenderers linear scan)
// ---------------------------------------------------------------------------

const enterPlanModeRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'EnterPlanMode')
    if (!toolUse)
      return null
    return renderEnterPlanMode(toolUse as Record<string, unknown>, context)
  },
}

const skillRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'Skill')
    if (!toolUse)
      return null
    return renderSkill(toolUse as Record<string, unknown>, context)
  },
}

const exitPlanModeRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'ExitPlanMode')
    if (!toolUse)
      return null
    return renderExitPlanMode(toolUse as Record<string, unknown>, context)
  },
}

const todoWriteRenderer: MessageContentRenderer = {
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

const askUserQuestionRenderer: MessageContentRenderer = {
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

const taskOutputRenderer: MessageContentRenderer = {
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
const taskNotificationRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_notification')
      return null

    const summary = typeof parsed.summary === 'string' ? parsed.summary : 'Task notification'
    const outputFile = typeof parsed.output_file === 'string' ? parsed.output_file : null

    return (
      <div class={toolMessage}>
        <div class={toolUseHeader}>
          <span class={inlineFlex} title="Task Notification">
            <Terminal size={16} class={toolUseIcon} />
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

/** Handles assistant messages: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}} */
const assistantTextRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || !isObject(parsed.message))
      return null
    const content = (parsed.message as Record<string, unknown>).content
    if (!Array.isArray(content))
      return null
    const text = content
      .filter((c: unknown) => isObject(c) && c.type === 'text')
      .map((c: unknown) => (c as Record<string, unknown>).text)
      .join('')
    if (!text)
      return null
    return <div class={markdownClass(role)} innerHTML={renderMarkdown(text)} />
  },
}

/** Inner component for thinking messages — owns local expand/collapse state. */
function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  const [expanded, setExpanded] = createSignal(false)

  return (
    <>
      <div class={thinkingHeader} onClick={() => setExpanded(prev => !prev)}>
        <span class={inlineFlex} title="Thinking">
          <Brain size={16} class={toolUseIcon} />
        </span>
        <span class={toolInputDetail}>Thinking</span>
        <span class={inlineFlex}>
          {expanded()
            ? <ChevronDown size={14} class={toolUseIcon} />
            : <ChevronRight size={14} class={toolUseIcon} />}
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <div class={markdownContent} innerHTML={renderMarkdown(props.text)} />
        </div>
      </Show>
    </>
  )
}

/** Handles assistant thinking messages: {"type":"assistant","message":{"content":[{"type":"thinking","thinking":"..."}]}} */
const assistantThinkingRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || !isObject(parsed.message))
      return null
    const content = (parsed.message as Record<string, unknown>).content
    if (!Array.isArray(content))
      return null
    const thinkingBlock = content.find(
      (c: unknown) => isObject(c) && c.type === 'thinking',
    ) as Record<string, unknown> | undefined
    if (!thinkingBlock)
      return null
    const text = String(thinkingBlock.thinking || '')
    if (!text)
      return null
    return <ThinkingMessage text={text} context={context} />
  },
}

/** Renders task_started system messages as a minimal "Task started" line (thread child). */
const taskStartedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_started')
      return null

    return (
      <div class={toolMessage}>
        <div class={toolUseHeader}>
          <span class={inlineFlex} title="Task Started">
            <Bot size={16} class={toolUseIcon} />
          </span>
          <span class={toolInputDetail}>Task started</span>
        </div>
      </div>
    )
  },
}

/**
 * Handles user messages with string content: {"type":"user","message":{"content":"..."}}
 * This covers local slash command responses (e.g. /context) whose message.content
 * is a plain string rather than an array of content blocks. If the content is
 * wrapped in <local-command-stdout> tags, the inner text is extracted and rendered
 * as markdown.
 */
const userTextContentRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || parsed.type !== 'user')
      return null

    const message = parsed.message as Record<string, unknown>
    if (!isObject(message))
      return null

    const content = message.content
    if (typeof content !== 'string')
      return null

    // Extract text between <local-command-stdout> tags if present.
    const startTag = '<local-command-stdout>'
    const endTag = '</local-command-stdout>'
    const startIdx = content.indexOf(startTag)
    const endIdx = content.indexOf(endTag)
    const text = startIdx !== -1 && endIdx !== -1 && endIdx > startIdx
      ? content.slice(startIdx + startTag.length, endIdx).trim()
      : content

    if (!text)
      return null

    return <div class={markdownClass(role)} innerHTML={renderMarkdown(text)} />
  },
}

/** Handles user messages: {"content":"..."} */
const userContentRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || typeof parsed.content !== 'string' || 'type' in parsed)
      return null
    return <div class={markdownClass(role)} innerHTML={renderMarkdown(parsed.content as string)} />
  },
}

// ---------------------------------------------------------------------------
// Dispatch map — O(1) renderer lookup by MessageCategory kind
// ---------------------------------------------------------------------------

/** Specialized tool renderers keyed by tool name. */
const SPECIALIZED_TOOL_RENDERERS: Record<string, (toolUse: Record<string, unknown>, context?: RenderContext) => JSX.Element | null> = {
  EnterPlanMode: renderEnterPlanMode,
  ExitPlanMode: renderExitPlanMode,
  TodoWrite: renderTodoWrite,
  AskUserQuestion: renderAskUserQuestion,
  TaskOutput: renderTaskOutput,
  Skill: renderSkill,
}

/** Dispatch rendering for a tool_use category: try specialized renderer first, then generic. */
function dispatchToolUse(
  category: Extract<MessageCategory, { kind: 'tool_use' }>,
  parsed: unknown,
  role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  const specialized = SPECIALIZED_TOOL_RENDERERS[category.toolName]
  if (specialized) {
    const result = specialized(category.toolUse, context)
    if (result !== null)
      return result
  }
  return toolUseRenderer.render(parsed, role, context)
}

/** Renderer functions keyed by MessageCategory kind. */
const KIND_RENDERERS: Record<string, (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null> = {
  // Wrap in arrow functions to avoid accessing cross-module `const` exports
  // at module initialization time, which can hit the TDZ due to the circular
  // dependency between messageRenderers ↔ toolRenderers.
  tool_result: (p, r, c) => toolResultRenderer.render(p, r, c),
  assistant_text: assistantTextRenderer.render,
  assistant_thinking: assistantThinkingRenderer.render,
  user_text: userTextContentRenderer.render,
  user_content: userContentRenderer.render,
  task_notification: taskNotificationRenderer.render,
  notification: (parsed, role, context) => {
    // Try each notification renderer in order
    return settingsChangedRenderer.render(parsed, role, context)
      ?? interruptedRenderer.render(parsed, role, context)
      ?? contextClearedRenderer.render(parsed, role, context)
      ?? agentRenamedRenderer.render(parsed, role, context)
      ?? rateLimitRenderer.render(parsed, role, context)
      ?? compactBoundaryRenderer.render(parsed, role, context)
      ?? microcompactBoundaryRenderer.render(parsed, role, context)
      ?? systemInitRenderer.render(parsed, role, context)
      ?? null
  },
  result_divider: resultRenderer.render,
  control_response: controlResponseRenderer.render,
  compact_summary: () => <span />,
  hidden: () => <span />,
}

/**
 * Dispatch-based rendering using a pre-computed MessageCategory.
 * Returns null only for 'unknown' kind (caller should fall back to linear scan).
 */
function dispatchRender(
  category: MessageCategory,
  parsed: unknown,
  role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  if (category.kind === 'tool_use')
    return dispatchToolUse(category, parsed, role, context)

  const renderer = KIND_RENDERERS[category.kind]
  if (renderer)
    return renderer(parsed, role, context)

  return null
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Fallback renderer list for linear scan when O(1) dispatch doesn't match.
 * Lazily initialised on first access to avoid TDZ errors from the circular
 * dependency between messageRenderers ↔ toolRenderers.
 */
let _fallbackRenderers: MessageContentRenderer[] | null = null
function getFallbackRenderers(): MessageContentRenderer[] {
  if (!_fallbackRenderers) {
    _fallbackRenderers = [
      exitPlanModeRenderer,
      enterPlanModeRenderer,
      skillRenderer,
      todoWriteRenderer,
      askUserQuestionRenderer,
      taskOutputRenderer,
      toolUseRenderer,
      toolResultRenderer,
      userTextContentRenderer,
      assistantTextRenderer,
      assistantThinkingRenderer,
      userContentRenderer,
      taskNotificationRenderer,
      taskStartedRenderer,
      settingsChangedRenderer,
      interruptedRenderer,
      contextClearedRenderer,
      agentRenamedRenderer,
      rateLimitRenderer,
      compactBoundaryRenderer,
      microcompactBoundaryRenderer,
      systemInitRenderer,
      resultRenderer,
      controlResponseRenderer,
    ]
  }
  return _fallbackRenderers
}

/**
 * Render a message's content.
 *
 * When a `category` is provided (from `classifyMessage()`), rendering uses O(1)
 * dispatch instead of iterating through the renderer chain. The linear scan is
 * used as a fallback for 'unknown' categories and for thread children that don't
 * have a pre-computed category.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  role: MessageRole,
  context?: RenderContext,
  category?: MessageCategory,
): JSX.Element {
  try {
    const parsed = typeof parsedOrRawJson === 'string'
      ? JSON.parse(parsedOrRawJson)
      : parsedOrRawJson

    // Fast path: O(1) dispatch when category is available
    if (category && category.kind !== 'unknown') {
      const result = dispatchRender(category, parsed, role, context)
      if (result !== null)
        return result
    }

    // Fallback: linear scan through renderer chain
    for (const renderer of getFallbackRenderers()) {
      const result = renderer.render(parsed, role, context)
      if (result !== null)
        return result
    }
  }
  catch { /* fall through to raw text */ }
  return <span>{typeof parsedOrRawJson === 'string' ? parsedOrRawJson : JSON.stringify(parsedOrRawJson)}</span>
}
