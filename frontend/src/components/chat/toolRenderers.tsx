/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from './diffUtils'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { ParsedCatLine } from './ReadResultView'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { BashInput, EditInput, GrepInput, WriteInput } from '~/types/toolMessages'
import Bot from 'lucide-solid/icons/bot'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import ChevronsRight from 'lucide-solid/icons/chevrons-right'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import ClockFading from 'lucide-solid/icons/clock-fading'
import Columns2 from 'lucide-solid/icons/columns-2'
import Copy from 'lucide-solid/icons/copy'
import File from 'lucide-solid/icons/file'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FoldVertical from 'lucide-solid/icons/fold-vertical'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import Hand from 'lucide-solid/icons/hand'
import ListTodo from 'lucide-solid/icons/list-todo'
import MessageSquare from 'lucide-solid/icons/message-square'
import OctagonX from 'lucide-solid/icons/octagon-x'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import PocketKnife from 'lucide-solid/icons/pocket-knife'
import Quote from 'lucide-solid/icons/quote'
import Rows2 from 'lucide-solid/icons/rows-2'
import Search from 'lucide-solid/icons/search'
import Stamp from 'lucide-solid/icons/stamp'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import UnfoldVertical from 'lucide-solid/icons/unfold-vertical'
import Vote from 'lucide-solid/icons/vote'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { parseMessageContent } from '~/lib/messageParser'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown, shikiHighlighter } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject, relativizePath } from './messageUtils'
import { parseCatNContent, ReadResultView } from './ReadResultView'
import { RelativeTime } from './RelativeTime'
import { formatDuration, formatTaskStatus, formatToolInput } from './rendererUtils'
import { spanColorKey } from './SpanLines'
import { spanLineColors } from './SpanLines.css'
import { renderToolDetail } from './toolDetailRenderers'
import { useSharedExpandedState } from './messageRenderers'
import {
  controlResponseTag,
  toolBodyContent,
  toolHeaderActions,
  toolHeaderTimestamp,
  toolInputSummary,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultContentAnsi,
  toolResultContentPre,
  toolResultError,
  toolResultPrompt,
  toolUseHeader,
  toolUseIcon,
  webSearchLink,
  webSearchLinkDomain,
  webSearchLinkList,
  webSearchLinkTitle,
} from './toolStyles.css'

/** Inline control response tag (Approved / Rejected) for tool headers. */
export function ControlResponseTag(props: { response?: { action: string, comment: string } }): JSX.Element {
  return (
    <Show when={props.response}>
      {cr => (
        <Tooltip text={cr().comment || undefined}>
          <span class={controlResponseTag}>
            {'\u00B7 '}
            {cr().action === 'approved' ? 'Approved' : cr().comment ? `Rejected: ${cr().comment}` : 'Rejected'}
          </span>
        </Tooltip>
      )}
    </Show>
  )
}

/** Shared layout for tool_use messages. Renders header boilerplate and optional body. */
export function ToolUseLayout(props: {
  /** Lucide icon component (e.g. ListTodo, Vote, SquareTerminal). */
  icon: LucideIcon
  /** Tool name, used as the title attribute on the icon. */
  toolName: string
  /** Primary title shown in the header. String auto-wraps in toolInputText; JSX renders as-is. */
  title: string | JSX.Element
  /** Summary line shown below header inside the bordered area, always visible even when collapsed. */
  summary?: JSX.Element
  /** Body content shown below the header. */
  children?: JSX.Element
  /** If true, body is always visible (not gated by expand). Default: false. */
  alwaysVisible?: boolean
  /** If true, body gets left border. Default: true. */
  bordered?: boolean
  /** Whether this tool has a diff to show (Edit tool). */
  hasDiff?: boolean
  /** Current diff view mode. */
  diffView?: DiffViewPreference
  /** Toggle diff view between unified and split. */
  onDiffViewChange?: (view: DiffViewPreference) => void
  context?: RenderContext
  /** Whether the body is expanded. */
  expanded?: boolean
  /** Toggle expand/collapse. When provided, shows the expand button. */
  onToggleExpand?: () => void
  /** Custom label for the expand button tooltip. */
  expandLabel?: string
  /** Copy content callback. */
  onCopyContent?: () => void
  contentCopied?: boolean
  copyContentLabel?: string
}): JSX.Element {
  const expanded = () => props.expanded ?? false
  const hasActions = () => !!props.onToggleExpand || !!props.context?.onCopyJson || !!props.hasDiff || !!props.onCopyContent
  return (
    <div class={toolMessage} data-tool-message>
      <div class={toolUseHeader}>
        <Tooltip text={props.toolName}>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={props.icon} size="md" />
          </span>
        </Tooltip>
        {typeof props.title === 'string'
          ? <span class={toolInputText}>{props.title}</span>
          : props.title}
        <Show when={props.context && hasActions()}>
          <ToolHeaderActions
            createdAt={props.context!.createdAt}
            expanded={expanded()}
            onToggleExpand={props.onToggleExpand}
            expandLabel={props.expandLabel}
            onCopyContent={props.onCopyContent}
            contentCopied={props.contentCopied}
            copyContentLabel={props.copyContentLabel}
            onCopyJson={props.context!.onCopyJson}
            jsonCopied={props.context!.jsonCopied ?? false}
            hasDiff={props.hasDiff}
            diffView={props.diffView}
            onToggleDiffView={props.onDiffViewChange ? () => props.onDiffViewChange!(props.diffView === 'unified' ? 'split' : 'unified') : undefined}
          />
        </Show>
      </div>
      <Show when={props.summary || (props.children && (props.alwaysVisible || expanded()))}>
        <div class={props.bordered !== false
          ? `${toolBodyContent}${props.context?.spanColor != null && props.context.spanColor > 0
            ? ` ${spanLineColors[spanColorKey(props.context.spanColor)]}`
            : ''}`
          : undefined}
        >
          <Show when={props.summary}>{props.summary}</Show>
          <Show when={props.children && (props.alwaysVisible || expanded())}>
            {props.children}
          </Show>
        </div>
      </Show>
    </div>
  )
}

/** Map tool name to its Lucide icon component. */
export function toolIconFor(name: string): LucideIcon {
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
    default: return ChevronsRight
  }
}

/** Actions area in tool header: Reply + Raw JSON copy + diff toggle + expand/collapse, all with tooltips. */
export function ToolHeaderActions(props: {
  /** ISO timestamp for relative time display. */
  createdAt?: string
  /** Whether the content is expanded. */
  expanded?: boolean
  /** Toggle expand/collapse. When set, shows the expand button. */
  onToggleExpand?: () => void
  /** Custom label for the expand tooltip (default: "Expand" / "Collapse"). */
  expandLabel?: string
  onCopyJson?: () => void
  jsonCopied?: boolean
  /** Whether this tool has a diff to show (Edit tool). */
  hasDiff?: boolean
  /** Current diff view mode. */
  diffView?: DiffViewPreference
  /** Toggle diff view between unified and split. */
  onToggleDiffView?: () => void
  /** Copy content callback — when provided, shows a copy button (command, file content, diff). */
  onCopyContent?: () => void
  contentCopied?: boolean
  /** Custom label for the copy content tooltip. */
  copyContentLabel?: string
  /** Reply callback — when provided, shows a reply button. */
  onReply?: () => void
  /** Copy markdown callback — when provided, shows a copy markdown button. */
  onCopyMarkdown?: () => void
  markdownCopied?: boolean
}): JSX.Element {
  const timestamp = () => props.createdAt
  return (
    <div class={toolHeaderActions} data-testid="message-toolbar">
      <Show when={props.onReply}>
        <IconButton
          icon={Quote}
          size="sm"
          data-testid="message-quote"
          onClick={() => props.onReply?.()}
          title="Quote"
        />
      </Show>
      <Show when={timestamp()}>
        <RelativeTime
          timestamp={timestamp()!}
          class={toolHeaderTimestamp}
        />
      </Show>
      <Show when={props.onCopyMarkdown}>
        <IconButton
          icon={props.markdownCopied ? Check : Copy}
          size="sm"
          data-testid="message-copy-markdown"
          onClick={() => props.onCopyMarkdown?.()}
          title={props.markdownCopied ? 'Copied' : 'Copy Markdown'}
        />
      </Show>
      <Show when={props.onCopyJson}>
        <IconButton
          icon={props.jsonCopied ? Check : Braces}
          size="sm"
          data-testid="message-copy-json"
          onClick={() => props.onCopyJson?.()}
          title={props.jsonCopied ? 'Copied' : 'Copy Raw JSON'}
        />
      </Show>
      <Show when={props.onCopyContent}>
        <IconButton
          icon={props.contentCopied ? Check : Copy}
          size="sm"
          onClick={() => props.onCopyContent?.()}
          title={props.contentCopied ? 'Copied' : (props.copyContentLabel || 'Copy')}
        />
      </Show>
      <Show when={props.hasDiff && props.onToggleDiffView}>
        <IconButton
          icon={props.diffView === 'unified' ? Columns2 : Rows2}
          size="sm"
          onClick={() => props.onToggleDiffView!()}
          title={props.diffView === 'unified' ? 'Switch to split view' : 'Switch to unified view'}
        />
      </Show>
      <Show when={props.onToggleExpand}>
        <IconButton
          icon={props.expanded ? FoldVertical : UnfoldVertical}
          size="sm"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            props.onToggleExpand!()
          }}
          title={props.expanded ? 'Collapse' : (props.expandLabel || 'Expand')}
        />
      </Show>
    </div>
  )
}

/** Local diff-view preference state, shared by ToolUseMessage and ToolResultMessage. */
function useDiffViewToggle(contextDiffView: () => DiffViewPreference | undefined) {
  const [localDiffView, setLocalDiffView] = createSignal<DiffViewPreference | null>(null)
  const diffView = () => localDiffView() ?? contextDiffView() ?? 'unified'
  const toggleDiffView = () => setLocalDiffView(diffView() === 'unified' ? 'split' : 'unified')
  return { diffView, toggleDiffView }
}

/** Inner component for tool_use messages — owns local diff view state. */
function ToolUseMessage(props: {
  toolName: string
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
  const [expanded, setExpanded] = useSharedExpandedState(props.context, 'tool-use-layout')
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
      icon={toolIconFor(props.toolName)}
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
        {/* eslint-disable-next-line solid/no-innerhtml -- shiki output is safe */}
        <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(props.fullCommand!)} />
      </Show>
    </ToolUseLayout>
  )
}

export function renderBashHighlight(code: string): string {
  return shikiHighlighter.codeToHtml(code, {
    lang: 'bash',
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
  })
}

/** Derive a summary element for a generic tool_use (Bash command, search paths). */
function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  switch (toolName) {
    case 'Bash': {
      const cmd = (input as BashInput).command
      if (!cmd)
        return undefined
      const firstLine = cmd.split('\n')[0]
      // eslint-disable-next-line solid/no-innerhtml -- shiki output is safe
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

/** Handles tool_use messages in assistant content array with per-tool icons and rich display. */
export const toolUseRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null

    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use')
    if (!toolUse)
      return null

    const toolData = toolUse as Record<string, unknown>
    const toolName = String(toolData.name || 'Unknown')
    const input = isObject(toolData.input) ? toolData.input as Record<string, unknown> : {}
    const detail = renderToolDetail(toolName, input, context)
    const summary = deriveToolSummary(toolName, input, context)
    const fallbackDisplay = detail ? null : formatToolInput(toolData.input)

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
      oldStr = ''
      newStr = writeInput.content ?? ''
      filePath = writeInput.file_path ?? ''
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
  },
}

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

/** Set of tool names whose results should be rendered as preformatted text. */
const PRE_TEXT_TOOLS = new Set(['Bash', 'Grep', 'Glob', 'Read', 'TaskOutput'])

/** Number of lines/items shown when a tool result is collapsed (last row fades out). */
export const COLLAPSED_RESULT_ROWS = 3

const TOOL_USE_ERROR_RE = /<tool_use_error>([\s\S]*?)<\/tool_use_error>/
const LEADING_BLANK_LINES_RE = /^(?:\s*\n)+/

/** Extract error text from <tool_use_error> tags in tool result content. */
function extractToolUseError(content: string): string | null {
  const match = content.match(TOOL_USE_ERROR_RE)
  return match ? match[1].trim() : null
}

export function stripLeadingBlankLines(content: string): string {
  return content.replace(LEADING_BLANK_LINES_RE, '')
}

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
      {/* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */}
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

/** Extract domain from a URL for display. */
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
        {/* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */}
        <div class={toolResultContent} innerHTML={renderMarkdown(props.summary)} />
      </Show>
    </div>
  )
}

/** Format byte count as a human-readable string. */
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
        {/* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */}
        <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.result)} />
      </Show>
    </div>
  )
}

/** Renders agent_prompt messages (prompt sent to sub-agent) as a collapsible tool-style block. */
export const agentPromptRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
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
  },
}

/** Collapsed agent prompt view: MessageSquare icon + "Prompt" title + collapsed markdown body. */
function AgentPromptView(props: {
  text: string
  context?: RenderContext
}): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(props.context, 'agent-prompt')
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
      {/* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */}
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.text)} />
    </ToolUseLayout>
  )
}

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
          /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderAnsi, not user input */
          ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayOutput())} />
          : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayOutput()}</div>}
      </Show>
    </div>
  )
}

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

/** Render Read tool results with syntax highlighting, or fall back to plain pre text. */
function renderReadOrPre(
  toolName: string,
  resultContent: string,
  readFilePath?: string,
  collapsed?: boolean,
): JSX.Element {
  if (toolName === 'Read') {
    const parsed = parseCatNContent(resultContent)
    if (parsed) {
      const displayLines = collapsed && parsed.length > COLLAPSED_RESULT_ROWS
        ? parsed.slice(0, COLLAPSED_RESULT_ROWS)
        : parsed
      const isCollapsed = collapsed && parsed.length > COLLAPSED_RESULT_ROWS
      return (
        <div class={isCollapsed ? toolResultCollapsed : undefined}>
          <ReadResultView lines={displayLines} filePath={readFilePath} />
        </div>
      )
    }
  }
  return <div class={toolResultContentPre}>{resultContent}</div>
}

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
          {/* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */}
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

/** Inner component for tool_result messages with structuredPatch — owns local diff view state. */
export function ToolResultMessage(props: {
  toolName: string
  resultContent: string
  isPreText: boolean
  structuredPatch: StructuredPatchHunk[] | null
  oldStr: string
  newStr: string
  filePath: string
  originalFile?: string
  /** File path for Read tool syntax highlighting. */
  readFilePath?: string
  /** Whether the tool result is an error (from is_error field). */
  isError?: boolean
  /** Optional status detail shown inline with the Bash-like result header. */
  statusDetail?: string
  context?: RenderContext
}): JSX.Element {
  const diffView = () => props.context?.diffView ?? 'unified'
  const hasPatch = () => !!props.structuredPatch && props.structuredPatch.length > 0
  const hasFallbackDiff = () => props.oldStr !== '' && props.newStr !== '' && props.oldStr !== props.newStr
  const hasDiff = () => hasPatch() || hasFallbackDiff()
  const errorText = () => extractToolUseError(props.resultContent)

  // Bash/TaskOutput/Read: collapsible via expand/collapse button in MessageBubble toolbar.
  const isRead = () => props.toolName === 'Read'
  const isBashLike = () => props.toolName === 'Bash' || props.toolName === 'TaskOutput' || props.toolName === ''
  const normalizedResultContent = () => isBashLike() ? stripLeadingBlankLines(props.resultContent) : props.resultContent
  const expanded = () => props.context?.toolResultExpanded ?? false
  const resultLines = () => normalizedResultContent().split('\n')
  const isCollapsed = () => {
    if (!isBashLike() || expanded())
      return false
    return resultLines().length > COLLAPSED_RESULT_ROWS
  }
  const displayContent = () => {
    if (!isCollapsed())
      return normalizedResultContent()
    return resultLines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  const statusIcon = () => props.isError ? CircleAlert : Check

  return (
    <div class={toolMessage} data-tool-message>
      <Show when={isBashLike() && props.isError !== undefined}>
        <div class={toolUseHeader}>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={statusIcon()} size="md" />
          </span>
          <span class={toolInputText}>
            {props.isError ? 'Error' : 'Success'}
            <Show when={props.statusDetail}>
              {detail => ` (${detail()})`}
            </Show>
          </span>
        </div>
      </Show>
      <Show
        when={!errorText()}
        fallback={<div class={toolResultError}>{errorText()}</div>}
      >
        <Show
          when={hasDiff()}
          fallback={
            props.isPreText
              ? isBashLike()
                ? containsAnsi(normalizedResultContent())
                  /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderAnsi, not user input */
                  ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayContent())} />
                  : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayContent()}</div>
                : renderReadOrPre(props.toolName, props.resultContent, props.readFilePath, isRead() && !expanded())
              /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */
              : <div class={toolResultContent} innerHTML={renderMarkdown(props.resultContent)} />
          }
        >
          <DiffView
            hunks={hasPatch() ? props.structuredPatch! : rawDiffToHunks(props.oldStr, props.newStr)}
            view={diffView()}
            filePath={props.filePath}
            originalFile={props.originalFile}
          />
        </Show>
      </Show>
    </div>
  )
}

/** Handles tool_result messages - note: type is "user" but message is from agent */
export const toolResultRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
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
  },
}
