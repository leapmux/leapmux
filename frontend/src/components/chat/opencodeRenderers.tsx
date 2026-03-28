/* eslint-disable solid/no-innerhtml -- HTML is produced via remark/shiki, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import File from 'lucide-solid/icons/file'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import ListTodo from 'lucide-solid/icons/list-todo'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import Wrench from 'lucide-solid/icons/wrench'
import { createSignal, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { TodoList } from '~/components/todo/TodoList'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { markdownContent } from './markdownContent.css'
import { ThinkingMessage, useSharedExpandedState } from './messageRenderers'
import { resultDivider } from './messageStyles.css'
import { isObject, relativizePath } from './messageUtils'
import { COLLAPSED_RESULT_ROWS, renderBashHighlight, stripLeadingBlankLines, ToolUseLayout } from './toolRenderers'
import {
  toolInputCode,
  toolInputPath,
  toolInputSummary,
  toolInputText,
  toolResultCollapsed,
  toolResultContentAnsi,
  toolResultContentPre,
  toolResultPrompt,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'

/** Icon for a tool kind. */
function kindIcon(kind: string | undefined): typeof Terminal {
  switch (kind) {
    case 'execute': return Terminal
    case 'edit': return FileEdit
    case 'read': return File
    case 'search': return Search
    default: return Wrench
  }
}

/** Capitalize a tool kind for display as a tool name. */
function kindLabel(kind: string | undefined): string {
  if (!kind)
    return 'Tool'
  return kind.charAt(0).toUpperCase() + kind.slice(1)
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

// ---------------------------------------------------------------------------
// Kind-specific title builders
// ---------------------------------------------------------------------------

/** Build a rich title element for search kind (pattern in monospace). */
function searchTitle(rawInput: Record<string, unknown> | undefined, fallbackTitle: string, context?: RenderContext): string | JSX.Element {
  const pattern = typeof rawInput?.pattern === 'string' ? rawInput.pattern as string : ''
  const path = typeof rawInput?.path === 'string' ? rawInput.path as string : ''
  if (!pattern)
    return fallbackTitle
  return (
    <>
      <span class={toolInputCode}>{`"${pattern}"`}</span>
      <Show when={path}>
        <span class={toolInputText}>{` ${relativizePath(path, context?.workingDir, context?.homeDir)}`}</span>
      </Show>
    </>
  )
}

/** Build a rich title element for read kind (file path + line range). */
function readTitle(rawInput: Record<string, unknown> | undefined, fallbackTitle: string, context?: RenderContext): string | JSX.Element {
  const filePath = typeof rawInput?.filePath === 'string' ? rawInput.filePath as string : ''
  if (!filePath)
    return fallbackTitle
  const offset = typeof rawInput?.offset === 'number' ? rawInput.offset as number : 0
  const limit = typeof rawInput?.limit === 'number' ? rawInput.limit as number : 0
  const rangeStr = offset && limit
    ? ` (Line ${offset}\u2013${offset + limit - 1})`
    : limit
      ? ` (Line 1\u2013${limit})`
      : offset
        ? ` (Line ${offset}\u2013)`
        : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(filePath, context?.workingDir, context?.homeDir)}</span>
      <Show when={rangeStr}>
        <span class={toolInputText}>{rangeStr}</span>
      </Show>
    </>
  )
}

/** Build a summary for search kind showing match count. */
function searchSummary(rawOutput: Record<string, unknown> | undefined): JSX.Element | undefined {
  const metadata = rawOutput && isObject(rawOutput.metadata) ? rawOutput.metadata as Record<string, unknown> : undefined
  const matches = typeof metadata?.matches === 'number' ? metadata.matches as number : -1
  if (matches < 0)
    return undefined
  if (matches === 0)
    return <div class={toolResultPrompt}>No matches found</div>
  return <div class={toolResultPrompt}>{`Found ${matches} match${matches === 1 ? '' : 'es'}`}</div>
}

// ---------------------------------------------------------------------------
// Public renderers
// ---------------------------------------------------------------------------

/** Render an OpenCode agent_message_chunk as markdown. */
export function opencodeAgentMessageRenderer(parsed: unknown): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <div class={markdownContent} innerHTML={renderMarkdown(text)} />
}

/** Render an OpenCode agent_thought_chunk as collapsible thinking (same style as Claude Code). */
export function opencodeThoughtRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <ThinkingMessage text={text} context={context} />
}

/** Render an OpenCode tool_call (pending status) — like Claude Code's tool_use header. */
export function opencodeToolCallRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  const kind = toolUse.kind as string | undefined
  const title = (toolUse.title as string | undefined) || kind || 'Tool'
  const icon = kindIcon(kind)

  return (
    <ToolUseLayout
      icon={icon}
      toolName={kindLabel(kind)}
      title={title}
      context={context}
    />
  )
}

/**
 * Inner component for OpenCode tool_call_update messages.
 * Handles all kinds (execute, search, read, edit, etc.) with kind-specific
 * title, summary, expand/collapse, and body rendering — mirroring Claude Code's
 * tool_use + tool_result combined in a single message.
 */
function ToolCallUpdateMessage(props: {
  toolUse: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const kind = () => props.toolUse.kind as string | undefined
  const rawInput = () => isObject(props.toolUse.rawInput) ? props.toolUse.rawInput as Record<string, unknown> : undefined
  const rawOutput = () => isObject(props.toolUse.rawOutput) ? props.toolUse.rawOutput as Record<string, unknown> : undefined
  const metadata = () => {
    const ro = rawOutput()
    return ro && isObject(ro.metadata) ? ro.metadata as Record<string, unknown> : undefined
  }

  const icon = () => kindIcon(kind())
  const command = () => typeof rawInput()?.command === 'string' ? rawInput()!.command as string : ''
  const description = () => typeof rawInput()?.description === 'string' ? rawInput()!.description as string : ''
  const fallbackTitle = () => description() || (props.toolUse.title as string | undefined) || kind() || 'Tool'

  const output = () => extractToolOutput(props.toolUse)
  const outputText = () => stripLeadingBlankLines(output().text)
  const exitCode = () => typeof metadata()?.exit === 'number' ? metadata()!.exit as number : null

  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'opencode-tool-call-update')
  const [commandCopied, setCommandCopied] = createSignal(false)

  // Output collapsing
  const outputLines = () => outputText().split('\n')
  const isOutputCollapsed = () => !expanded() && outputLines().length > COLLAPSED_RESULT_ROWS
  const displayOutput = () => {
    if (!isOutputCollapsed())
      return outputText()
    return outputLines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  // Kind-specific title
  const title = (): string | JSX.Element => {
    switch (kind()) {
      case 'search': return searchTitle(rawInput(), fallbackTitle(), props.context)
      case 'read': return readTitle(rawInput(), fallbackTitle(), props.context)
      default: return fallbackTitle()
    }
  }

  // Kind-specific summary
  const summary = (): JSX.Element | undefined => {
    switch (kind()) {
      case 'execute': {
        const cmd = command()
        if (!cmd)
          return undefined
        return <div class={toolInputSummary} innerHTML={renderBashHighlight(cmd.split('\n')[0])} />
      }
      case 'search':
        return searchSummary(rawOutput())
      default:
        return undefined
    }
  }

  // Expand/collapse
  const isMultiLineCommand = () => command().includes('\n')
  const hasExpandable = () => isMultiLineCommand() || outputLines().length > COLLAPSED_RESULT_ROWS
  const expandLabel = () => isMultiLineCommand() ? 'Show full command' : 'Expand output'

  // Status line (execute kind)
  const isError = () => output().error || (exitCode() !== null && exitCode() !== 0)
  const statusIcon = () => isError() ? CircleAlert : Check
  const statusLabel = () => {
    if (output().error)
      return 'Error'
    if (exitCode() !== null && exitCode() !== 0)
      return `Error (exit ${exitCode()})`
    return 'Success'
  }

  // Execute-specific: hide summary when expanded + multi-line command
  const displaySummary = () => expanded() && isMultiLineCommand() ? undefined : summary()

  // Diff view
  const hasDiff = () => !!output().diff

  return (
    <ToolUseLayout
      icon={icon()}
      toolName={kindLabel(kind())}
      title={title()}
      summary={displaySummary()}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={hasExpandable() ? () => setExpanded(v => !v) : undefined}
      expandLabel={expandLabel()}
      onCopyContent={command()
        ? () => {
            navigator.clipboard.writeText(command())
            setCommandCopied(true)
            setTimeout(setCommandCopied, 2000, false)
          }
        : undefined}
      contentCopied={commandCopied()}
      copyContentLabel="Copy Command"
      alwaysVisible
    >
      {/* Execute: full multi-line command (when expanded) */}
      <Show when={kind() === 'execute' && expanded() && isMultiLineCommand()}>
        <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(command())} />
      </Show>

      {/* Execute: status header (Success/Error) */}
      <Show when={kind() === 'execute' && outputText()}>
        <div class={toolUseHeader}>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={statusIcon()} size="md" />
          </span>
          <span class={toolInputText}>{statusLabel()}</span>
        </div>
      </Show>

      {/* Diff view (edit kind) */}
      <Show when={hasDiff()}>
        <DiffView
          hunks={rawDiffToHunks(output().diff!.oldText, output().diff!.newText)}
          view="unified"
          filePath={output().diff!.path}
        />
      </Show>

      {/* Text output (all kinds except when diff is shown) */}
      <Show when={outputText() && !hasDiff()}>
        {containsAnsi(outputText())
          ? <div class={`${toolResultContentAnsi}${isOutputCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayOutput())} />
          : <div class={`${toolResultContentPre}${isOutputCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayOutput()}</div>}
      </Show>

      {/* Error indicator when no output text */}
      <Show when={output().error && !outputText() && !hasDiff()}>
        <div class={toolUseHeader}>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={CircleAlert} size="md" />
          </span>
          <span class={toolInputText}>Failed</span>
        </div>
      </Show>
    </ToolUseLayout>
  )
}

/** Render an OpenCode tool_call_update (completed/failed) — combined tool_use + tool_result. */
export function opencodeToolCallUpdateRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  return <ToolCallUpdateMessage toolUse={toolUse} context={context} />
}

/** Render an OpenCode result divider (turn completion). */
export function opencodeResultDividerRenderer(parsed: unknown): JSX.Element | null {
  if (!isObject(parsed))
    return null
  const obj = parsed as Record<string, unknown>
  const reason = obj.stopReason as string | undefined
  const label = reason && reason !== 'end_turn' ? `Turn ended (${reason})` : 'Turn ended'
  return <div class={resultDivider}>{label}</div>
}

/** Render an OpenCode plan (todo list). */
export function opencodePlanRenderer(toolUse: Record<string, unknown>, _role: MessageRole, context?: RenderContext): JSX.Element {
  const entries = toolUse.entries as Array<{ priority?: string, status?: string, content: string }> | undefined

  if (!entries || entries.length === 0)
    return <></>

  const todos = entries.map(e => ({
    content: e.content,
    status: e.status === 'completed' ? 'completed' as const : 'pending' as const,
  }))

  const entriesToMarkdown = () => entries.map(e => `- [${e.status === 'completed' ? 'x' : ' '}] ${e.content}`).join('\n')
  const [copied, setCopied] = createSignal(false)
  const copyMarkdown = () => {
    void navigator.clipboard.writeText(entriesToMarkdown())
    setCopied(true)
    setTimeout(setCopied, 2000, false)
  }
  const reply = context?.onReply ? () => context.onReply!(entriesToMarkdown()) : undefined

  return (
    <ToolUseLayout
      icon={ListTodo}
      toolName="Plan"
      title="Plan"
      context={context}
      onReply={reply}
      onCopyMarkdown={copyMarkdown}
      markdownCopied={copied()}
    >
      <TodoList items={todos} />
    </ToolUseLayout>
  )
}
