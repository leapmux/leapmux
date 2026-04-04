/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from '../diffUtils'
import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import type { ParsedCatLine } from '../ReadResultView'
import type { AgentChatMessage, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { TodoItem } from '~/stores/chat.store'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, TaskStopInput, ToolSearchInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import ChevronsRight from 'lucide-solid/icons/chevrons-right'
import ClockFading from 'lucide-solid/icons/clock-fading'
import File from 'lucide-solid/icons/file'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import Hand from 'lucide-solid/icons/hand'
import ListTodo from 'lucide-solid/icons/list-todo'
import MessageSquare from 'lucide-solid/icons/message-square'
import OctagonX from 'lucide-solid/icons/octagon-x'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import PocketKnife from 'lucide-solid/icons/pocket-knife'
import Search from 'lucide-solid/icons/search'
import Stamp from 'lucide-solid/icons/stamp'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import Vote from 'lucide-solid/icons/vote'
import Wrench from 'lucide-solid/icons/wrench'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { TodoList } from '~/components/todo/TodoList'
import { useCopyButton } from '~/hooks/useCopyButton'
import { parseMessageContent, todosToMarkdown } from '~/lib/messageParser'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from '../diffUtils'
import { markdownContent } from '../markdownContent.css'
import { useSharedExpandedState } from '../messageRenderers'
import { getAssistantContent, isObject, relativizePath } from '../messageUtils'
import { parseCatNContent, ReadResultView } from '../ReadResultView'
import { formatDuration, formatTaskStatus, formatToolInput } from '../rendererUtils'
import {
  renderAgentDetail,
  renderBashDetail,
  renderEditDetail,
  renderGlobDetail,
  renderGrepDetail,
  renderReadDetail,
  renderWebFetchDetail,
  renderWebSearchDetail,
  renderWriteDetail,
} from '../toolDetailRenderers'
import {
  COLLAPSED_RESULT_ROWS,
  EmptyTodoLayout,
  renderBashHighlight,
  ToolResultMessage,
  ToolUseLayout,
  useDiffViewToggle,
} from '../toolRenderers'
import {
  toolInputCode,
  toolInputSummary,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultContentAnsi,
  toolResultContentPre,
  toolResultPrompt,
  toolUseHeader,
  toolUseIcon,
  webSearchLink,
  webSearchLinkDomain,
  webSearchLinkList,
  webSearchLinkTitle,
} from '../toolStyles.css'

// ---------------------------------------------------------------------------
// MCP utilities
// ---------------------------------------------------------------------------

const MCP_PREFIX = 'mcp__'

function isMcpTool(name: string): boolean {
  return name.startsWith(MCP_PREFIX)
}

function parseMcpToolName(name: string): { serverName: string, toolName: string } | null {
  const parts = name.split('__')
  const [mcpPart, serverName, ...toolNameParts] = parts
  if (mcpPart !== 'mcp' || !serverName)
    return null
  const toolName = toolNameParts.length > 0 ? toolNameParts.join('__') : undefined
  if (!toolName)
    return null
  return { serverName, toolName }
}

function formatMcpDisplayName(serverName: string, toolName: string): string {
  const humanServer = serverName
    .split('_')
    .map(w => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
  return `${humanServer} / ${toolName}`
}

// ---------------------------------------------------------------------------
// toolIconFor
// ---------------------------------------------------------------------------

function toolIconFor(name: string): LucideIcon {
  switch (name) {
    case 'Bash': return Terminal
    case 'Read': return File
    case 'Write': return FilePlus
    case 'Edit': return FilePen
    case 'Grep': return TextSearch
    case 'Glob': return FolderSearch
    case 'Task': return Bot
    case 'Agent': return Bot
    case 'WebFetch': return Globe
    case 'WebSearch': return Globe
    case 'TodoWrite': return ListTodo
    case 'EnterPlanMode': return TicketsPlane
    case 'ExitPlanMode': return PlaneTakeoff
    case 'AskUserQuestion': return Vote
    case 'TaskOutput': return ClockFading
    case 'Skill': return PocketKnife
    case 'ToolSearch': return Search
    case 'TaskStop': return OctagonX
    default: return isMcpTool(name) ? Wrench : ChevronsRight
  }
}

// ---------------------------------------------------------------------------
// renderClaudeToolDetail
// ---------------------------------------------------------------------------

/** Prefer common parameter names for the hint, then fall back to first short string. */
const HINT_KEYS = ['query', 'input', 'prompt', 'text', 'command', 'description', 'url']

function extractInputHint(input: Record<string, unknown>): string {
  for (const key of HINT_KEYS) {
    const val = input[key]
    if (typeof val === 'string' && val.length > 0 && val.length <= 120)
      return val.length > 80 ? `${val.slice(0, 80)}…` : val
  }
  for (const val of Object.values(input)) {
    if (typeof val === 'string' && val.length > 0 && val.length <= 120)
      return val.length > 80 ? `${val.slice(0, 80)}…` : val
  }
  return ''
}

function renderClaudeToolDetail(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const cwd = context?.workingDir
  const homeDir = context?.homeDir

  switch (toolName) {
    // Shared tool detail primitives
    case 'Bash': return renderBashDetail(input as BashInput)
    case 'Read': return renderReadDetail(input as ReadInput, cwd, homeDir)
    case 'Write': return renderWriteDetail(input as WriteInput, cwd, homeDir)
    case 'Edit': return renderEditDetail(input as EditInput, cwd, homeDir)
    case 'Grep': return renderGrepDetail(input as GrepInput)
    case 'Glob': return renderGlobDetail(input as GlobInput, cwd, homeDir)
    case 'WebFetch': return renderWebFetchDetail(input as WebFetchInput)
    case 'WebSearch': return renderWebSearchDetail(input as WebSearchInput)
    case 'Agent':
    case 'Task': return renderAgentDetail(input, toolName)

    // Claude-only tool details
    case 'TaskOutput': {
      const { task_id, block, timeout } = input as { task_id?: string, block?: boolean, timeout?: number }
      const parts: string[] = []
      if (task_id)
        parts.push(`task ID: ${task_id}`)
      if (typeof timeout === 'number')
        parts.push(`timeout: ${timeout >= 1000 ? `${timeout / 1000}s` : `${timeout}ms`}`)
      if (block !== undefined)
        parts.push(`block: ${block}`)
      const meta = parts.length > 0 ? ` (${parts.join(' \u00B7 ')})` : ''
      return <span class={toolInputText}>{`Waiting for output${meta}`}</span>
    }
    case 'ToolSearch': {
      const { query } = input as ToolSearchInput
      return query
        ? <span class={toolInputCode}>{`"${query}"`}</span>
        : null
    }
    case 'TaskStop': {
      const { task_id: taskId } = input as TaskStopInput
      return taskId
        ? <span class={toolInputText}>{`Stop task ${taskId}`}</span>
        : <span class={toolInputText}>Stop task</span>
    }
    case 'EnterPlanMode':
      return <span class={toolInputText}>Entering Plan Mode</span>
    case 'Skill': {
      const skillName = String(input.skill || '')
      return <span class={toolInputText}>{`Skill: /${skillName}`}</span>
    }
    default: {
      const hint = extractInputHint(input)
      const mcpInfo = parseMcpToolName(toolName)
      if (mcpInfo) {
        const displayName = formatMcpDisplayName(mcpInfo.serverName, mcpInfo.toolName)
        return (
          <>
            <span class={toolInputText}>{displayName}</span>
            {hint ? <span class={toolInputCode}>{` "${hint}"`}</span> : null}
          </>
        )
      }
      // Unknown non-MCP tool — show tool name with hint if available.
      return hint
        ? <span class={toolInputText}>{`${toolName}: ${hint}`}</span>
        : <span class={toolInputText}>{toolName}</span>
    }
  }
}

// ---------------------------------------------------------------------------
// ToolUseMessage (inner component for tool_use)
// ---------------------------------------------------------------------------

/** Inner component for tool_use messages — owns local diff view state. */
function ToolUseMessage(props: {
  toolName: string
  icon: LucideIcon
  detail: JSX.Element | null
  /** Summary shown below header inside the bordered area (e.g. Bash command, Grep result count). */
  summary?: JSX.Element | null
  /** Full command text for Bash (shown when expanded). */
  fullCommand?: string
  fallbackDisplay: string | null
  hasDiff: boolean
  oldStr: string
  newStr: string
  filePath: string
  /** Original file content before edit (for expandable context lines). */
  originalFile?: string
  /** If true, body is always visible (not gated by expand). */
  alwaysVisible?: boolean
  context?: RenderContext
}): JSX.Element {
  const { diffView, toggleDiffView } = useDiffViewToggle(() => props.context?.diffView)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'tool-use-layout')
  const [commandCopied, setCommandCopied] = createSignal(false)

  const title = () => props.detail ?? `${props.toolName}${props.fallbackDisplay || ''}`

  // Edit diffs are collapsed by default (the tool_result already shows the diff).
  // Write diffs and non-diff tool_use messages remain always visible.
  const isCollapsibleDiff = () => props.hasDiff && !props.alwaysVisible
  // Bash: collapsible when command is multi-line.
  const isMultiLineCommand = () => !!props.fullCommand && props.fullCommand.includes('\n')
  const isCollapsible = () => isCollapsibleDiff() || isMultiLineCommand()

  return (
    <ToolUseLayout
      icon={props.icon}
      toolName={props.toolName}
      title={title()}
      summary={isMultiLineCommand() && expanded() ? undefined : props.summary}
      alwaysVisible={props.alwaysVisible}
      hasDiff={props.hasDiff}
      diffView={diffView()}
      onDiffViewChange={toggleDiffView}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={isCollapsible() ? () => setExpanded(v => !v) : undefined}
      expandLabel={isMultiLineCommand() ? 'Show full command' : 'Show diff'}
      onCopyContent={props.fullCommand
        ? () => {
            navigator.clipboard.writeText(props.fullCommand!)
            setCommandCopied(true)
            setTimeout(setCommandCopied, 2000, false)
          }
        : undefined}
      contentCopied={commandCopied()}
      copyContentLabel="Copy Command"
    >
      <Show when={props.hasDiff}>
        <DiffView
          hunks={rawDiffToHunks(props.oldStr, props.newStr)}
          view={diffView()}
          filePath={props.filePath}
          originalFile={props.originalFile}
        />
      </Show>
      <Show when={isMultiLineCommand() && expanded()}>
        <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(props.fullCommand!)} />
      </Show>
    </ToolUseLayout>
  )
}

// ---------------------------------------------------------------------------
// deriveToolSummary
// ---------------------------------------------------------------------------

/** Derive a summary element for a generic tool_use (Bash command, search paths). */
function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  switch (toolName) {
    case 'Bash': {
      const cmd = (input as BashInput).command
      if (!cmd)
        return undefined
      const firstLine = cmd.split('\n')[0]
      return <div class={toolInputSummary} innerHTML={renderBashHighlight(firstLine)} />
    }
    case 'Grep': {
      const path = (input as GrepInput).path
      if (!path)
        return undefined
      return <div class={toolInputSummary}>{relativizePath(path, context?.workingDir, context?.homeDir)}</div>
    }
    default:
      return undefined
  }
}

// ---------------------------------------------------------------------------
// extractToolUseInfo
// ---------------------------------------------------------------------------

/** Extract tool name and input from a tool_use AgentChatMessage. */
function extractToolUseInfo(msg: AgentChatMessage): { toolName: string, input: Record<string, unknown> } | null {
  const parsed = parseMessageContent(msg)
  const obj = parsed.parentObject
  if (!obj)
    return null
  const content = getAssistantContent(obj)
  if (!content)
    return null
  const toolUse = content.find(c => isObject(c) && c.type === 'tool_use')
  if (!toolUse)
    return null
  const toolData = toolUse as Record<string, unknown>
  return {
    toolName: String(toolData.name || ''),
    input: isObject(toolData.input) ? toolData.input as Record<string, unknown> : {},
  }
}

// ---------------------------------------------------------------------------
// Tool result constants and helpers
// ---------------------------------------------------------------------------

/** Set of tool names whose results should be rendered as preformatted text. */
const PRE_TEXT_TOOLS = new Set(['Bash', 'Grep', 'Glob', 'Read', 'TaskOutput'])

/**
 * Summary line patterns found at the start of raw Grep/Glob tool output.
 * When tool_use_result is absent (e.g. subagent), the raw text starts with
 * a summary line like "Found 21 files" followed by the actual file list.
 * This regex matches those summary lines so they can be stripped from the
 * file list and the count can be extracted.
 */
const RAW_RESULT_SUMMARY_RE = /^(?:Found (\d+) (?:files?|lines?(?:\s+and\s+\d+\s+files?)?)|(\d+) match(?:es)? in (\d+) files?|No (?:matches|files) found)$/

/** Grep content-mode line pattern: "line_num:text" or "file:line_num:text". */
const GREP_CONTENT_LINE_RE = /^\d+[:-]|^[^:]+:\d+[:-]/

/**
 * Parse raw Grep/Glob result text (without tool_use_result).
 * Strips the leading summary line (if any) and returns structured data
 * matching what tool_use_result would provide.
 */
function parseRawGrepGlobResult(raw: string, toolName: string): {
  numFiles: number
  numLines: number
  filenames: string[]
  content: string
} {
  const lines = raw.split('\n')
  const firstLine = lines[0]?.trim() ?? ''
  const summaryMatch = firstLine.match(RAW_RESULT_SUMMARY_RE)

  // Strip the summary line from the data lines.
  const dataLines = summaryMatch ? lines.slice(1) : lines
  const nonEmpty = dataLines.filter(l => l.trim())

  // For Grep content mode (lines contain "file:line:match" or "line_num:text"),
  // we check the first few lines to classify the output format.
  const sampleLines = nonEmpty.length > 5 ? nonEmpty.slice(0, 5) : nonEmpty
  const looksLikeContent = toolName === 'Grep'
    && sampleLines.length > 0
    && sampleLines.every(l => GREP_CONTENT_LINE_RE.test(l))

  let numFiles = 0
  let numLines = 0

  if (summaryMatch) {
    if (summaryMatch[1]) {
      // "Found N files" or "Found N lines"
      const n = Number.parseInt(summaryMatch[1], 10)
      if (firstLine.includes('line')) {
        numLines = n
      }
      else {
        numFiles = n
      }
    }
    else if (summaryMatch[2] && summaryMatch[3]) {
      // "N matches in M files"
      numLines = Number.parseInt(summaryMatch[2], 10)
      numFiles = Number.parseInt(summaryMatch[3], 10)
    }
  }

  if (looksLikeContent) {
    return {
      numFiles: numFiles || 0,
      numLines: numLines || nonEmpty.length,
      filenames: [],
      content: nonEmpty.join('\n'),
    }
  }

  return {
    numFiles: numFiles || nonEmpty.length,
    numLines: 0,
    filenames: nonEmpty,
    content: '',
  }
}

// ---------------------------------------------------------------------------
// Result view components
// ---------------------------------------------------------------------------

/** Reusable file-path list used by Grep/Glob result views. */
function FileListView(props: {
  filenames: string[]
  context?: RenderContext
}): JSX.Element {
  return (
    <div class={toolResultContentPre}>
      <For each={props.filenames}>
        {(f, i) => (
          <>
            {i() > 0 && '\n'}
            {relativizePath(f, props.context?.workingDir, props.context?.homeDir)}
          </>
        )}
      </For>
    </div>
  )
}

/** ToolSearch result view showing matched tool names. */
function ToolSearchResultView(props: {
  matches: string[]
}): JSX.Element {
  return (
    <div class={toolMessage}>
      <Show
        when={props.matches.length > 0}
        fallback={<div class={toolResultContentPre}>No tools found</div>}
      >
        <div class={toolResultContentPre}>
          <For each={props.matches}>
            {(name, i) => (
              <>
                {i() > 0 && '\n'}
                {name}
              </>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

/** Structured Grep result view for the expanded thread child. */
function GrepResultView(props: {
  numFiles: number
  numLines: number
  filenames: string[]
  content: string
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const hasResult = () => props.numFiles > 0 || props.numLines > 0
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded()
    && (props.filenames.length > COLLAPSED_RESULT_ROWS
      || props.content.split('\n').length > COLLAPSED_RESULT_ROWS)
  const displayFilenames = () => {
    if (expanded() || props.filenames.length <= COLLAPSED_RESULT_ROWS)
      return props.filenames
    return props.filenames.slice(0, COLLAPSED_RESULT_ROWS)
  }
  const displayContent = () => {
    if (expanded() || !props.content)
      return props.content
    const lines = props.content.split('\n')
    if (lines.length <= COLLAPSED_RESULT_ROWS)
      return props.content
    return lines.slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  const summary = () => {
    if (props.numLines > 0 && props.numFiles > 0)
      return `${props.numLines} match${props.numLines === 1 ? '' : 'es'} in ${props.numFiles} file${props.numFiles === 1 ? '' : 's'}`
    if (props.numFiles > 0)
      return `Found ${props.numFiles} file${props.numFiles === 1 ? '' : 's'}`
    return ''
  }

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show
        when={hasResult()}
        fallback={<div class={toolResultContentPre}>{props.fallbackContent || 'No matches found'}</div>}
      >
        <Show when={summary()}>
          <div class={toolResultPrompt}>{summary()}</div>
        </Show>
        <Show when={displayFilenames().length > 0}>
          <FileListView filenames={displayFilenames()} context={props.context} />
        </Show>
        <Show when={displayContent()}>
          <div class={toolResultContentPre}>{displayContent()}</div>
        </Show>
      </Show>
    </div>
  )
}

/** Structured Glob result view for the expanded thread child. */
function GlobResultView(props: {
  filenames: string[]
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded() && props.filenames.length > COLLAPSED_RESULT_ROWS
  const displayFilenames = () => {
    if (expanded() || props.filenames.length <= COLLAPSED_RESULT_ROWS)
      return props.filenames
    return props.filenames.slice(0, COLLAPSED_RESULT_ROWS)
  }

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show
        when={props.filenames.length > 0}
        fallback={<div class={toolResultContentPre}>{props.fallbackContent || 'No files found'}</div>}
      >
        <FileListView filenames={displayFilenames()} context={props.context} />
      </Show>
    </div>
  )
}

/** Format agent status for display. */
function formatAgentStatus(status: string): string {
  if (status === 'async_launched')
    return 'launched asynchronously'
  return status
}

/** Collapsed Agent result view: icon + "Agent {agentId} {status}" header + collapsed markdown body. */
function AgentResultView(props: {
  agentId: string
  status: string
  content: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded() && props.content.split('\n').length > COLLAPSED_RESULT_ROWS
  const icon = () => props.status === 'completed' ? Check : Bot

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={`${inlineFlex} ${toolUseIcon}`}>
          <Icon icon={icon()} size="md" />
        </span>
        <span class={toolInputText}>{`Agent ${props.agentId} ${formatAgentStatus(props.status)}`}</span>
      </div>
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.content)} />
    </div>
  )
}

/** AskUserQuestion result view: shows questions with selected answers. */
function AskUserQuestionResultView(props: {
  toolUseResult: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const questions = () => Array.isArray(props.toolUseResult.questions)
    ? props.toolUseResult.questions as Array<Record<string, unknown>>
    : []
  const answers = () => isObject(props.toolUseResult.answers)
    ? props.toolUseResult.answers as Record<string, string>
    : {}

  return (
    <div class={toolMessage}>
      <For each={questions()}>
        {(q) => {
          const header = String(q.header || '')
          const answer = answers()[header]
          return (
            <div class={toolResultPrompt}>
              <strong>
                {header}
                {': '}
              </strong>
              {answer || <em>Not answered</em>}
            </div>
          )
        }}
      </For>
    </div>
  )
}

const WWW_PREFIX_RE = /^www\./

function extractDomain(url: string): string {
  try {
    return new URL(url).hostname.replace(WWW_PREFIX_RE, '')
  }
  catch {
    return url
  }
}

interface WebSearchLink {
  title: string
  url: string
}

/** Extract deduplicated links from WebSearch tool_use_result.results. */
function extractWebSearchLinks(results: unknown[]): WebSearchLink[] {
  const seen = new Set<string>()
  const links: WebSearchLink[] = []
  for (const item of results) {
    if (isObject(item) && Array.isArray((item as Record<string, unknown>).content)) {
      for (const link of (item as Record<string, unknown>).content as Array<Record<string, unknown>>) {
        if (isObject(link) && typeof link.url === 'string' && typeof link.title === 'string' && !seen.has(link.url)) {
          seen.add(link.url)
          links.push({ title: link.title, url: link.url })
        }
      }
    }
  }
  return links
}

/** Extract the final text summary from WebSearch results (last string entry). */
function extractWebSearchSummary(results: unknown[]): string {
  for (let i = results.length - 1; i >= 0; i--) {
    if (typeof results[i] === 'string' && (results[i] as string).trim().length > 0)
      return (results[i] as string).trim()
  }
  return ''
}

/** WebSearch result view: collapsed by default, shows links + summary. */
function WebSearchResultView(props: {
  links: WebSearchLink[]
  summary: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded() && props.links.length > COLLAPSED_RESULT_ROWS
  const displayLinks = () => {
    if (expanded() || props.links.length <= COLLAPSED_RESULT_ROWS)
      return props.links
    return props.links.slice(0, COLLAPSED_RESULT_ROWS)
  }

  return (
    <div class={toolMessage}>
      <Show when={props.links.length > 0}>
        <div class={toolResultPrompt}>
          {`${props.links.length} result${props.links.length === 1 ? '' : 's'}`}
        </div>
        <div class={`${webSearchLinkList}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
          <For each={displayLinks()}>
            {link => (
              <div class={webSearchLink}>
                <span class={webSearchLinkTitle}>
                  <a href={link.url} target="_blank" rel="noopener noreferrer nofollow">{link.title}</a>
                </span>
                <span class={webSearchLinkDomain}>{extractDomain(link.url)}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show when={expanded() && props.summary}>
        <div class={toolResultContent} innerHTML={renderMarkdown(props.summary)} />
      </Show>
    </div>
  )
}

function formatBytes(bytes: number): string {
  if (bytes < 1024)
    return `${bytes} B`
  if (bytes < 1024 * 1024)
    return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** WebFetch result view: shows HTTP status, size, duration, then collapsed markdown body. */
function WebFetchResultView(props: {
  code: number
  codeText: string
  bytes: number
  durationMs: number
  result: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded() && props.result.split('\n').length > COLLAPSED_RESULT_ROWS
  const summary = () => {
    const parts: string[] = []
    parts.push(`${props.code} ${props.codeText}`)
    if (props.bytes > 0)
      parts.push(formatBytes(props.bytes))
    if (props.durationMs > 0)
      parts.push(formatDuration(props.durationMs))
    return parts.join(' \u00B7 ')
  }

  return (
    <div class={toolMessage}>
      <div class={toolResultPrompt}>{summary()}</div>
      <Show when={props.result}>
        <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.result)} />
      </Show>
    </div>
  )
}

// ---------------------------------------------------------------------------
// AgentPromptView
// ---------------------------------------------------------------------------

/** Collapsed agent prompt view: MessageSquare icon + "Prompt" title + collapsed markdown body. */
function AgentPromptView(props: {
  text: string
  context?: RenderContext
}): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'agent-prompt')
  const isCollapsed = () => !expanded() && props.text.split('\n').length > COLLAPSED_RESULT_ROWS

  return (
    <ToolUseLayout
      icon={MessageSquare}
      toolName="Prompt"
      title="Prompt"
      context={props.context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.text)} />
    </ToolUseLayout>
  )
}

// ---------------------------------------------------------------------------
// TaskOutputResultView
// ---------------------------------------------------------------------------

/** Structured TaskOutput result view using tool_use_result.task data. */
function TaskOutputResultView(props: {
  task: Record<string, unknown>
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const status = () => typeof props.task.status === 'string' ? props.task.status : ''
  const statusLabel = () => formatTaskStatus(status() || undefined)
  const description = () => typeof props.task.description === 'string' ? props.task.description : ''
  const taskId = () => typeof props.task.task_id === 'string' ? props.task.task_id : ''
  const exitCode = () => typeof props.task.exitCode === 'number' ? props.task.exitCode : null
  const output = () => typeof props.task.output === 'string' ? props.task.output : props.fallbackContent
  const icon = () => status() === 'completed' ? Check : ClockFading

  const meta = () => {
    const parts: string[] = []
    if (taskId())
      parts.push(`task ID: ${taskId()}`)
    if (exitCode() !== null)
      parts.push(`exit code: ${exitCode()}`)
    return parts.length > 0 ? ` (${parts.join(' \u00B7 ')})` : ''
  }

  const title = () => {
    const label = statusLabel()
    const desc = description()
    if (label && desc)
      return `${label}: ${desc}${meta()}`
    if (label)
      return `${label}${meta()}`
    if (desc)
      return `${desc}${meta()}`
    return `TaskOutput${meta()}`
  }

  const outputLines = () => output().split('\n')
  const isCollapsed = () => !expanded() && outputLines().length > COLLAPSED_RESULT_ROWS
  const displayOutput = () => {
    if (!isCollapsed())
      return output()
    return outputLines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={`${inlineFlex} ${toolUseIcon}`}>
          <Icon icon={icon()} size="md" />
        </span>
        <span class={toolInputText}>{title()}</span>
      </div>
      <Show when={displayOutput()}>
        {containsAnsi(output())
          ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayOutput())} />
          : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayOutput()}</div>}
      </Show>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ReadFileResultView
// ---------------------------------------------------------------------------

/** Structured Read result view using tool_use_result.file data. */
function ReadFileResultView(props: {
  lines: ParsedCatLine[]
  filePath: string
  totalLines: number
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded ?? false
  const isCollapsed = () => !expanded() && props.lines.length > COLLAPSED_RESULT_ROWS
  const displayLines = () => {
    if (expanded() || props.lines.length <= COLLAPSED_RESULT_ROWS)
      return props.lines
    return props.lines.slice(0, COLLAPSED_RESULT_ROWS)
  }

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show
        when={props.lines.length > 0}
        fallback={<div class={toolResultContentPre}>{props.fallbackContent || 'Empty file'}</div>}
      >
        <ReadResultView lines={displayLines()} filePath={props.filePath} />
      </Show>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ExitPlanModeResultView
// ---------------------------------------------------------------------------

/** ExitPlanMode result view: "Plan approved" with file path, or "Sent feedback:" with markdown content. */
function ExitPlanModeResultView(props: {
  isError: boolean
  resultContent: string
  toolUseResult?: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const filePath = () => typeof props.toolUseResult?.filePath === 'string'
    ? props.toolUseResult.filePath as string
    : ''

  return (
    <Show
      when={!props.isError}
      fallback={(
        <div class={toolMessage}>
          <div class={toolUseHeader}>
            <span class={`${inlineFlex} ${toolUseIcon}`}>
              <Icon icon={Hand} size="md" />
            </span>
            <span class={toolInputText}>Sent feedback:</span>
          </div>
          <div class={markdownContent} innerHTML={renderMarkdown(props.resultContent)} />
        </div>
      )}
    >
      <div class={toolMessage}>
        <div class={toolUseHeader}>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={Stamp} size="md" />
          </span>
          <span class={toolInputText}>Plan approved</span>
        </div>
        <Show when={filePath()}>
          <div class={toolResultPrompt}>
            {'Plan file: '}
            <code>{relativizePath(filePath(), props.context?.workingDir, props.context?.homeDir)}</code>
          </div>
        </Show>
      </div>
    </Show>
  )
}

// ---------------------------------------------------------------------------
// renderTodoWrite
// ---------------------------------------------------------------------------

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

  if (count === 0)
    return <EmptyTodoLayout toolName="TodoWrite" context={context} />

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

// ---------------------------------------------------------------------------
// renderAskUserQuestion
// ---------------------------------------------------------------------------

/** Render AskUserQuestion tool_use with questions and options. Returns null if input is invalid. */
function renderAskUserQuestion(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
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

// ---------------------------------------------------------------------------
// renderExitPlanMode
// ---------------------------------------------------------------------------

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''
  const { copied, copy } = useCopyButton(() => planText || undefined)
  const reply = planText && context?.onReply ? () => context.onReply!(planText) : undefined

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="ExitPlanMode"
      title="Leaving Plan Mode"
      alwaysVisible={true}
      bordered={false}
      context={context}
      onReply={reply}
      onCopyMarkdown={planText ? copy : undefined}
      markdownCopied={copied()}
    >
      <Show when={planText}>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(planText)} />
      </Show>
    </ToolUseLayout>
  )
}

// ---------------------------------------------------------------------------
// renderClaudeMessage — main entry point
// ---------------------------------------------------------------------------

/** Dispatches rendering for all Claude Code message categories. */
export function renderClaudeMessage(
  category: MessageCategory,
  parsed: unknown,
  _role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  switch (category.kind) {
    case 'tool_use':
      return renderClaudeToolUse(category, parsed, context)
    case 'tool_result':
      return renderClaudeToolResult(parsed, context)
    case 'agent_prompt':
      return renderClaudeAgentPrompt(parsed, context)
    default:
      return null
  }
}

// ---------------------------------------------------------------------------
// tool_use dispatch (from toolUseRenderer + special tool_use renderers)
// ---------------------------------------------------------------------------

function renderClaudeToolUse(
  category: Extract<MessageCategory, { kind: 'tool_use' }>,
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  const toolName = category.toolName
  const toolUse = category.toolUse

  // Special tool_use renderers
  if (toolName === 'TodoWrite')
    return renderTodoWrite(toolUse, context)
  if (toolName === 'AskUserQuestion')
    return renderAskUserQuestion(toolUse, context)
  if (toolName === 'ExitPlanMode')
    return renderExitPlanMode(toolUse, context)

  // Generic tool_use rendering
  const input = isObject(toolUse.input) ? toolUse.input as Record<string, unknown> : {}
  const detail = renderClaudeToolDetail(toolName, input, context)
  const summary = deriveToolSummary(toolName, input, context)
  const fallbackDisplay = detail ? null : formatToolInput(toolUse.input)

  // Edit/Write tool: show diff view
  const isEdit = toolName === 'Edit'
  const isWrite = toolName === 'Write'
  let oldStr: string
  let newStr: string
  let filePath: string
  if (isEdit) {
    const editInput = input as EditInput
    oldStr = editInput.old_string ?? ''
    newStr = editInput.new_string ?? ''
    filePath = editInput.file_path ?? ''
  }
  else if (isWrite) {
    const writeInput = input as WriteInput
    filePath = writeInput.file_path ?? ''
    // When the linked tool_result indicates this was an update (not a new file),
    // hide the full file content — the tool_result already shows the diff.
    const resultObj = context?.toolResultMessage
      ? parseMessageContent(context.toolResultMessage).parentObject
      : undefined
    const resultToolUseResult = resultObj?.tool_use_result as Record<string, unknown> | undefined
    const isUpdate = resultToolUseResult?.type === 'update'
    oldStr = ''
    newStr = isUpdate ? '' : (writeInput.content ?? '')
  }
  else {
    oldStr = ''
    newStr = ''
    filePath = ''
  }
  const hasDiff = (isEdit && oldStr !== '' && newStr !== '' && oldStr !== newStr)
    || (isWrite && newStr !== '')

  // Bash: pass full command for multi-line expand.
  const fullCommand = toolName === 'Bash' ? (input as BashInput).command : undefined

  return (
    <ToolUseMessage
      toolName={toolName}
      icon={toolIconFor(toolName)}
      detail={detail}
      summary={summary}
      fullCommand={fullCommand}
      fallbackDisplay={fallbackDisplay}
      hasDiff={hasDiff}
      oldStr={oldStr}
      newStr={newStr}
      filePath={filePath}
      originalFile={undefined}
      alwaysVisible={isWrite}
      context={context}
    />
  )
}

// ---------------------------------------------------------------------------
// tool_result dispatch (from toolResultRenderer)
// ---------------------------------------------------------------------------

function renderClaudeToolResult(
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed) || parsed.type !== 'user')
    return null

  const message = parsed.message as Record<string, unknown>
  if (!isObject(message))
    return null

  const content = message.content
  if (!Array.isArray(content))
    return null

  // Find tool_result in content array
  const toolResult = content.find((c: unknown) =>
    isObject(c) && c.type === 'tool_result',
  )
  if (!toolResult)
    return null

  const resultData = toolResult as Record<string, unknown>
  const resultContent = Array.isArray(resultData.content)
    ? (resultData.content as Array<Record<string, unknown>>)
        .filter(c => isObject(c) && c.type === 'text')
        .map(c => c.text)
        .join('')
    : String(resultData.content || '')

  // Extract tool name: prefer span_type (always set for span messages),
  // then tool_use_result, then linked tool_use message.
  const toolUseResult = parsed.tool_use_result as Record<string, unknown> | undefined
  const toolUseInfo = context?.toolUseMessage ? extractToolUseInfo(context.toolUseMessage) : null
  const toolName = String(context?.spanType || toolUseResult?.tool_name || toolUseInfo?.toolName || context?.parentToolName || '')
  const toolInput = toolUseInfo?.input

  // Determine whether this tool's output should be preformatted text.
  // When tool name is unknown (standalone tool_result without parent context),
  // default to preformatted rendering since most tool outputs are plain text.
  const isPreText = toolName === '' || PRE_TEXT_TOOLS.has(toolName)

  // Extract structuredPatch from tool_use_result for Edit/Write diffs.
  // When rendered as a child of an Edit/Write tool_use, the parent already
  // shows the diff — skip extraction to avoid a duplicate.
  const isEditOrWrite = toolName === 'Edit' || toolName === 'Write'
  const parentShowsDiff = context?.parentToolName === 'Edit' || context?.parentToolName === 'Write'
  const structuredPatch = isEditOrWrite && !parentShowsDiff && Array.isArray(toolUseResult?.structuredPatch)
    ? (toolUseResult!.structuredPatch as StructuredPatchHunk[])
    : null
  const filePath = isEditOrWrite && !parentShowsDiff ? String(toolUseResult?.filePath || '') : ''
  const oldStr = isEditOrWrite && !parentShowsDiff ? String(toolUseResult?.oldString || '') : ''
  const newStr = isEditOrWrite && !parentShowsDiff ? String(toolUseResult?.newString || '') : ''
  const originalFile = isEditOrWrite && !parentShowsDiff && typeof toolUseResult?.originalFile === 'string'
    ? toolUseResult.originalFile as string
    : undefined

  // Extract structured file data from tool_use_result for Read results.
  const readFile = toolName === 'Read' && isObject(toolUseResult?.file)
    ? toolUseResult!.file as Record<string, unknown>
    : null
  const readFilePath = readFile
    ? String(readFile.filePath || '')
    : toolName === 'Read' ? String(toolInput?.file_path || '') : undefined

  // Hide redundant result content: Edit/Write success messages when the
  // parent already shows the diff.
  const hideContent = parentShowsDiff

  // Pre-parse raw Read content for the fallback branch (no structured data).
  const parsedReadLines = toolName === 'Read' && !readFile ? parseCatNContent(resultContent) : null

  // Build the inner result element.
  let innerResult: JSX.Element

  // Grep: render structured result view (with or without tool_use_result).
  if (toolName === 'Grep') {
    let numFiles: number
    let numLines: number
    let filenames: string[]
    let grepContent: string
    if (toolUseResult) {
      numFiles = typeof toolUseResult.numFiles === 'number' ? toolUseResult.numFiles : 0
      numLines = typeof toolUseResult.numLines === 'number' ? toolUseResult.numLines : 0
      filenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames as string[] : []
      grepContent = typeof toolUseResult.content === 'string' ? toolUseResult.content : ''
    }
    else {
      // Subagent: parse raw resultContent to extract summary and file list.
      const parsed = parseRawGrepGlobResult(resultContent, 'Grep')
      numFiles = parsed.numFiles
      numLines = parsed.numLines
      filenames = parsed.filenames
      grepContent = parsed.content
    }
    innerResult = (
      <GrepResultView
        numFiles={numFiles}
        numLines={numLines}
        filenames={filenames}
        content={grepContent}
        fallbackContent={resultContent}
        context={context}
      />
    )
  }
  // ToolSearch: render matched tool names from tool_use_result.
  else if (toolName === 'ToolSearch' && toolUseResult) {
    const matches = Array.isArray(toolUseResult.matches) ? toolUseResult.matches as string[] : []
    innerResult = <ToolSearchResultView matches={matches} />
  }
  // Read: render structured result view when tool_use_result has file data.
  else if (toolName === 'Read' && readFile) {
    const fileContent = typeof readFile.content === 'string' ? readFile.content : ''
    const startLine = typeof readFile.startLine === 'number' ? readFile.startLine : 1
    const totalLines = typeof readFile.totalLines === 'number' ? readFile.totalLines : 0
    const lines: ParsedCatLine[] = fileContent
      ? fileContent.split('\n').map((text, i) => ({ num: startLine + i, text }))
      : []
    innerResult = (
      <ReadFileResultView
        lines={lines}
        filePath={readFilePath!}
        totalLines={totalLines}
        fallbackContent={resultContent}
        context={context}
      />
    )
  }
  // Read without structured data (e.g. subagent): parse raw tab-delimited content.
  else if (parsedReadLines) {
    innerResult = (
      <ReadFileResultView
        lines={parsedReadLines}
        filePath={readFilePath || ''}
        totalLines={0}
        fallbackContent={resultContent}
        context={context}
      />
    )
  }
  // Agent: render collapsed result with "Agent {agentId} {status}" header.
  else if (toolName === 'Agent' && toolUseResult) {
    const agentId = typeof toolUseResult.agentId === 'string' ? toolUseResult.agentId : ''
    const status = typeof toolUseResult.status === 'string' ? toolUseResult.status : 'completed'
    const agentContent = Array.isArray(toolUseResult.content)
      ? (toolUseResult.content as Array<Record<string, unknown>>)
          .filter(c => isObject(c) && c.type === 'text')
          .map(c => String(c.text || ''))
          .join('\n\n')
      : resultContent
    innerResult = (
      <AgentResultView
        agentId={agentId}
        status={status}
        content={agentContent}
        context={context}
      />
    )
  }
  // TaskOutput: render structured result with status/description header.
  else if (toolName === 'TaskOutput' && toolUseResult && isObject(toolUseResult.task)) {
    innerResult = (
      <TaskOutputResultView
        task={toolUseResult.task as Record<string, unknown>}
        fallbackContent={resultContent}
        context={context}
      />
    )
  }
  // AskUserQuestion: render answered questions from tool_use_result.
  else if (toolName === 'AskUserQuestion' && toolUseResult) {
    innerResult = <AskUserQuestionResultView toolUseResult={toolUseResult} context={context} />
  }
  // WebFetch: render structured result view with HTTP status, size, duration.
  else if (toolName === 'WebFetch' && toolUseResult && typeof toolUseResult.code === 'number') {
    const fetchResult = typeof toolUseResult.result === 'string' ? toolUseResult.result : resultContent
    innerResult = (
      <WebFetchResultView
        code={toolUseResult.code as number}
        codeText={typeof toolUseResult.codeText === 'string' ? toolUseResult.codeText : ''}
        bytes={typeof toolUseResult.bytes === 'number' ? toolUseResult.bytes as number : 0}
        durationMs={typeof toolUseResult.durationMs === 'number' ? toolUseResult.durationMs as number : 0}
        result={fetchResult}
        context={context}
      />
    )
  }
  // WebSearch: render structured result view with links and summary.
  else if (toolName === 'WebSearch' && toolUseResult && Array.isArray(toolUseResult.results)) {
    const links = extractWebSearchLinks(toolUseResult.results as unknown[])
    const summary = extractWebSearchSummary(toolUseResult.results as unknown[])
    innerResult = (
      <WebSearchResultView
        links={links}
        summary={summary}
        context={context}
      />
    )
  }
  // ExitPlanMode: render approval or feedback.
  else if (toolName === 'ExitPlanMode') {
    const isError = resultData.is_error === true
    innerResult = (
      <ExitPlanModeResultView
        isError={isError}
        resultContent={resultContent}
        toolUseResult={toolUseResult}
        context={context}
      />
    )
  }
  // Glob: render structured result view (with or without tool_use_result).
  else if (toolName === 'Glob') {
    let filenames: string[]
    if (toolUseResult) {
      filenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames as string[] : []
    }
    else {
      // Subagent: parse raw resultContent to extract summary and file list.
      filenames = parseRawGrepGlobResult(resultContent, 'Glob').filenames
    }
    innerResult = (
      <GlobResultView
        filenames={filenames}
        fallbackContent={resultContent}
        context={context}
      />
    )
  }
  else {
    innerResult = (
      <ToolResultMessage
        toolName={toolName}
        resultContent={hideContent ? '' : resultContent}
        isPreText={isPreText}
        structuredPatch={structuredPatch}
        oldStr={oldStr}
        newStr={newStr}
        filePath={filePath}
        originalFile={originalFile}
        readFilePath={readFilePath}
        isError={typeof resultData.is_error === 'boolean' ? resultData.is_error : undefined}
        context={context}
      />
    )
  }

  return innerResult
}

// ---------------------------------------------------------------------------
// agent_prompt dispatch (from agentPromptRenderer)
// ---------------------------------------------------------------------------

function renderClaudeAgentPrompt(
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed) || parsed.type !== 'user' || typeof parsed.parent_tool_use_id !== 'string')
    return null

  const message = parsed.message as Record<string, unknown> | undefined
  if (!isObject(message))
    return null
  const content = (message as Record<string, unknown>).content
  if (!Array.isArray(content))
    return null

  const text = (content as Array<Record<string, unknown>>)
    .filter(c => isObject(c) && c.type === 'text')
    .map(c => String(c.text || ''))
    .join('\n\n')
  if (!text)
    return null

  return <AgentPromptView text={text} context={context} />
}
