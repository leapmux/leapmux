/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import Eye from 'lucide-solid/icons/eye'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import ListTodo from 'lucide-solid/icons/list-todo'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { TodoList } from '~/components/todo/TodoList'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { markdownContent } from './markdownContent.css'
import {
  thinkingChevron,
  thinkingChevronExpanded,
  thinkingContent,
  thinkingHeader,
} from './messageStyles.css'
import { isObject, relativizePath } from './messageUtils'
import { ToolResultMessage, ToolUseLayout } from './toolRenderers'
import {
  toolInputPath,
  toolInputSummary,
  toolMessage,
  toolResultContent,
  toolResultContentPre,
  toolResultError,
  toolUseIcon,
} from './toolStyles.css'

/** Icon for a tool kind. */
function kindIcon(kind: string | undefined): typeof Terminal {
  switch (kind) {
    case 'execute': return Terminal
    case 'edit': return FileEdit
    case 'read': return Eye
    case 'search': return Search
    default: return Wrench
  }
}

/** Extract text content from agent_message_chunk. */
function extractAgentText(parsed: unknown): string {
  if (!isObject(parsed))
    return ''
  const content = (parsed as Record<string, unknown>).content
  if (isObject(content))
    return String((content as Record<string, unknown>).text || '')
  return ''
}

/** Render an OpenCode agent_message_chunk as markdown (inner content only — bubble wrapping is handled by MessageBubble). */
export function opencodeAgentMessageRenderer(parsed: unknown): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <div class={markdownContent} innerHTML={renderMarkdown(text)} />
}

/** Render an OpenCode agent_thought_chunk as collapsible thinking. */
export function opencodeThoughtRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element {
  const text = extractAgentText(parsed)
  const [expanded, setExpanded] = createSignal(false)

  return (
    <div class={toolMessage}>
      <div class={thinkingHeader} onClick={() => setExpanded(!expanded())}>
        <Icon icon={Brain} size="sm" />
        <span>Thinking</span>
        <span class={expanded() ? thinkingChevronExpanded : thinkingChevron}>
          <Icon icon={ChevronRight} size="sm" />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>{text}</div>
      </Show>
    </div>
  )
}

/** Render an OpenCode tool_call (pending status). */
export function opencodeToolCallRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  const kind = toolUse.kind as string | undefined
  const title = toolUse.title as string | undefined || kind || 'Tool'
  const locations = toolUse.locations as Array<{ path: string }> | undefined
  const icon = kindIcon(kind)
  const stream = () => context?.stream as CommandStreamSegment[] | undefined

  return (
    <ToolUseLayout>
      <div class={toolInputSummary}>
        <span class={toolUseIcon}><Icon icon={icon} size="sm" /></span>
        <span>{title}</span>
        <Show when={locations && locations.length > 0}>
          <span class={toolInputPath}>{relativizePath(locations![0].path)}</span>
        </Show>
      </div>
      <Show when={stream() && stream()!.length > 0}>
        <div class={toolResultContent}>
          <For each={stream()!}>
            {segment => <div class={toolResultContentPre}>{segment.text}</div>}
          </For>
        </div>
      </Show>
    </ToolUseLayout>
  )
}

/** Extract text output from tool_call_update content array. */
function extractToolOutput(toolUse: Record<string, unknown>): { text: string, error: boolean, diff?: { path: string, oldText: string, newText: string } } {
  const contentArr = toolUse.content as unknown[] | undefined
  let text = ''
  let diff: { path: string, oldText: string, newText: string } | undefined
  if (Array.isArray(contentArr)) {
    for (const item of contentArr) {
      if (!isObject(item))
        continue
      const entry = item as Record<string, unknown>
      if (entry.type === 'content' && isObject(entry.content)) {
        const ct = entry.content as Record<string, unknown>
        text += String(ct.text || '')
      }
      if (entry.type === 'diff') {
        diff = {
          path: String(entry.path || ''),
          oldText: String(entry.oldText || ''),
          newText: String(entry.newText || ''),
        }
      }
    }
  }
  const rawOutput = toolUse.rawOutput as Record<string, unknown> | undefined
  if (!text && rawOutput) {
    text = String(rawOutput.output || rawOutput.error || '')
  }
  const status = toolUse.status as string | undefined
  return { text, error: status === 'failed', diff }
}

/** Render an OpenCode tool_call_update (completed/failed). */
export function opencodeToolCallUpdateRenderer(toolUse: Record<string, unknown>, _role: MessageRole, _context?: RenderContext): JSX.Element {
  const kind = toolUse.kind as string | undefined
  const title = toolUse.title as string | undefined || kind || 'Tool'
  const icon = kindIcon(kind)
  const output = extractToolOutput(toolUse)

  return (
    <ToolResultMessage>
      <div class={toolInputSummary}>
        <span class={toolUseIcon}><Icon icon={icon} size="sm" /></span>
        <span>{title}</span>
        <Show when={output.error}>
          <span class={toolResultError}> (failed)</span>
        </Show>
      </div>
      <Show when={output.diff}>
        {diffData => (
          <DiffView
            path={diffData().path}
            hunks={rawDiffToHunks(diffData().oldText, diffData().newText)}
          />
        )}
      </Show>
      <Show when={output.text}>
        <div class={toolResultContent}>
          <div class={toolResultContentPre}>{output.text}</div>
        </div>
      </Show>
    </ToolResultMessage>
  )
}

/** Render an OpenCode plan (todo list). */
export function opencodePlanRenderer(toolUse: Record<string, unknown>, _role: MessageRole, _context?: RenderContext): JSX.Element {
  const entries = toolUse.entries as Array<{ priority?: string, status?: string, content: string }> | undefined

  if (!entries || entries.length === 0)
    return <></>

  const todos = entries.map(e => ({
    content: e.content,
    status: e.status === 'completed' ? 'completed' as const : 'pending' as const,
  }))

  return (
    <ToolUseLayout>
      <div class={toolInputSummary}>
        <span class={toolUseIcon}><Icon icon={ListTodo} size="sm" /></span>
        <span>Plan</span>
      </div>
      <TodoList items={todos} />
    </ToolUseLayout>
  )
}
