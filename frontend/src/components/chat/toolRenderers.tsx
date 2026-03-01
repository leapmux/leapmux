/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from './diffUtils'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { BashInput, EditInput, GrepInput, WriteInput } from '~/types/toolMessages'
import Bot from 'lucide-solid/icons/bot'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import ChevronsRight from 'lucide-solid/icons/chevrons-right'
import Columns2 from 'lucide-solid/icons/columns-2'
import Copy from 'lucide-solid/icons/copy'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FileText from 'lucide-solid/icons/file-text'
import FoldVertical from 'lucide-solid/icons/fold-vertical'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import Quote from 'lucide-solid/icons/quote'
import Rows2 from 'lucide-solid/icons/rows-2'
import Search from 'lucide-solid/icons/search'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import Terminal from 'lucide-solid/icons/terminal'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import Toolbox from 'lucide-solid/icons/toolbox'
import UnfoldVertical from 'lucide-solid/icons/unfold-vertical'
import Vote from 'lucide-solid/icons/vote'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { getAssistantContent, isObject, relativizePath } from './messageUtils'
import { parseCatNContent, ReadResultView } from './ReadResultView'
import { RelativeTime } from './RelativeTime'
import { firstNonEmptyLine, formatDuration, formatGlobSummary, formatGrepSummary, formatNumber, formatTaskStatus, formatToolInput } from './rendererUtils'
import { renderToolDetail } from './toolDetailRenderers'
import {
  controlResponseTag,
  toolBodyContent,
  toolFileList,
  toolHeaderActions,
  toolHeaderTimestamp,
  toolInputPath,
  toolInputSummary,
  toolInputSummaryExpanded,
  toolInputText,
  toolMessage,
  toolResultContent,
  toolResultContentAnsi,
  toolResultContentPre,
  toolResultError,
  toolResultPrompt,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'

/** Inline control response tag (Approved / Rejected) for tool headers. */
export function ControlResponseTag(props: { response?: { action: string, comment: string } }): JSX.Element {
  return (
    <Show when={props.response}>
      {cr => (
        <span class={controlResponseTag} title={cr().comment || undefined}>
          {'\u00B7 '}
          {cr().action === 'approved' ? 'Approved' : cr().comment ? `Rejected: ${cr().comment}` : 'Rejected'}
        </span>
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
}): JSX.Element {
  const expanded = () => props.context?.threadExpanded ?? false
  const hasThread = () => !props.alwaysVisible && (props.context?.threadChildCount ?? 0) > 0
  const hasActions = () => hasThread() || !!props.context?.onCopyJson || !!props.hasDiff
  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={`${inlineFlex} ${toolUseIcon}`} title={props.toolName}>
          <Icon icon={props.icon} size="md" />
        </span>
        {typeof props.title === 'string'
          ? <span class={toolInputText}>{props.title}</span>
          : props.title}
        <ControlResponseTag response={props.context?.childControlResponse} />
        <Show when={props.context && hasActions()}>
          <ToolHeaderActions
            createdAt={props.context!.createdAt}
            updatedAt={props.context!.updatedAt}
            threadCount={props.alwaysVisible ? 0 : (props.context!.threadChildCount ?? 0)}
            threadExpanded={expanded()}
            onToggleThread={props.context!.onToggleThread ?? (() => {})}
            onCopyJson={props.context!.onCopyJson}
            jsonCopied={props.context!.jsonCopied ?? false}
            hasDiff={props.hasDiff}
            diffView={props.diffView}
            onToggleDiffView={props.onDiffViewChange ? () => props.onDiffViewChange!(props.diffView === 'unified' ? 'split' : 'unified') : undefined}
          />
        </Show>
      </div>
      <Show when={props.summary || (props.children && (props.alwaysVisible || expanded()))}>
        <div class={props.bordered !== false ? toolBodyContent : undefined}>
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
    case 'Read': return FileText
    case 'Write': return FilePlus
    case 'Edit': return FilePen
    case 'Grep': return Search
    case 'Glob': return FolderSearch
    case 'Task': return Bot
    case 'Agent': return Bot
    case 'WebFetch': return Globe
    case 'WebSearch': return Globe
    case 'TodoWrite': return ListTodo
    case 'EnterPlanMode': return TicketsPlane
    case 'ExitPlanMode': return PlaneTakeoff
    case 'AskUserQuestion': return Vote
    case 'TaskOutput': return SquareTerminal
    case 'Skill': return Toolbox
    default: return ChevronsRight
  }
}

/** Actions area in tool header: Reply + Raw JSON copy + diff toggle + thread expander, all with tooltips. */
export function ToolHeaderActions(props: {
  /** ISO timestamp for relative time display. */
  createdAt?: string
  /** ISO timestamp of the last update (thread merge). Preferred over createdAt when set. */
  updatedAt?: string
  threadCount: number
  threadExpanded: boolean
  onToggleThread: () => void
  onCopyJson?: () => void
  jsonCopied?: boolean
  /** Whether this tool has a diff to show (Edit tool). */
  hasDiff?: boolean
  /** Current diff view mode. */
  diffView?: DiffViewPreference
  /** Toggle diff view between unified and split. */
  onToggleDiffView?: () => void
  /** Reply callback — when provided, shows a reply button. */
  onReply?: () => void
  /** Copy markdown callback — when provided, shows a copy markdown button. */
  onCopyMarkdown?: () => void
  markdownCopied?: boolean
}): JSX.Element {
  const timestamp = () => props.updatedAt || props.createdAt
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
      <Show when={props.hasDiff && props.onToggleDiffView}>
        <IconButton
          icon={props.diffView === 'unified' ? Columns2 : Rows2}
          size="sm"
          onClick={() => props.onToggleDiffView!()}
          title={props.diffView === 'unified' ? 'Switch to split view' : 'Switch to unified view'}
        />
      </Show>
      <Show when={props.threadCount > 0}>
        <IconButton
          icon={props.threadExpanded ? FoldVertical : UnfoldVertical}
          size="sm"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            props.onToggleThread()
          }}
          title={props.threadExpanded ? 'Collapse' : `Expand ${props.threadCount} tool result${props.threadCount === 1 ? '' : 's'}`}
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

  const title = () => props.detail ?? `${props.toolName}${props.fallbackDisplay || ''}`

  return (
    <ToolUseLayout
      icon={toolIconFor(props.toolName)}
      toolName={props.toolName}
      title={title()}
      summary={props.summary}
      alwaysVisible={props.alwaysVisible}
      hasDiff={props.hasDiff}
      diffView={diffView()}
      onDiffViewChange={toggleDiffView}
      context={props.context}
    >
      <Show when={props.hasDiff}>
        <DiffView
          hunks={(props.context?.childStructuredPatch?.length ? props.context.childStructuredPatch : null) ?? rawDiffToHunks(props.oldStr, props.newStr)}
          view={diffView()}
          filePath={props.filePath}
          originalFile={props.originalFile}
        />
      </Show>
    </ToolUseLayout>
  )
}

/** Derive a summary element for a generic tool_use (Bash command, Grep/Glob result counts). */
function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  const content = context?.childResultContent

  switch (toolName) {
    case 'Bash': {
      const cmd = (input as BashInput).command
      if (!cmd)
        return undefined
      const expanded = context?.threadExpanded ?? false
      const cls = expanded ? toolInputSummaryExpanded : toolInputSummary
      if (expanded)
        return <div class={cls}>{cmd}</div>
      const firstLine = cmd.split('\n')[0]
      const truncated = firstLine.length > 120 ? `${firstLine.slice(0, 120)}\u2026` : firstLine
      return <div class={cls}>{truncated}</div>
    }
    case 'Grep': {
      const path = (input as GrepInput).path
      const pathLine = path
        ? relativizePath(path, context?.workingDir, context?.homeDir)
        : null
      const summaryText = formatGrepSummary(
        context?.childGrepNumFiles,
        context?.childGrepNumLines,
        content ? firstNonEmptyLine(content) : null,
      )
      if (!pathLine && !summaryText)
        return undefined
      return (
        <>
          {pathLine && <div class={toolInputSummary}>{pathLine}</div>}
          {summaryText && <div class={toolInputSummary}>{summaryText}</div>}
        </>
      )
    }
    case 'Glob': {
      const summaryText = formatGlobSummary(
        context?.childGlobNumFiles,
        context?.childGlobDurationMs,
        context?.childGlobTruncated,
        content ? firstNonEmptyLine(content) : null,
      )
      if (!summaryText)
        return undefined
      return <div class={toolInputSummary}>{summaryText}</div>
    }
    case 'TaskOutput': {
      const task = context?.childTask
      const parts: string[] = []
      if (task?.exitCode !== undefined)
        parts.push(`Exit code ${task.exitCode}`)
      if (task?.task_id)
        parts.push(`Task ID ${task.task_id}`)
      return parts.length > 0
        ? <div class={toolInputSummary}>{parts.join(' \u00B7 ')}</div>
        : undefined
    }
    case 'Agent':
    case 'Task': {
      const status = context?.childToolResultStatus
      const hasChildren = (context?.threadChildCount ?? 0) > 0
      const displayStatus = status
        ? formatTaskStatus(status)
        : (hasChildren ? 'Running' : null)
      const parts: string[] = []
      if (displayStatus)
        parts.push(displayStatus)
      if (context?.childTotalDurationMs !== undefined)
        parts.push(formatDuration(context.childTotalDurationMs))
      if (context?.childTotalTokens !== undefined)
        parts.push(`${formatNumber(context.childTotalTokens)} tokens`)
      if (context?.childTotalToolUseCount !== undefined)
        parts.push(`${context.childTotalToolUseCount} tool use${context.childTotalToolUseCount === 1 ? '' : 's'}`)
      return parts.length > 0
        ? <div class={toolInputSummary}>{parts.join(' \u00B7 ')}</div>
        : undefined
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

    return (
      <ToolUseMessage
        toolName={toolName}
        detail={detail}
        summary={summary}
        fallbackDisplay={fallbackDisplay}
        hasDiff={hasDiff}
        oldStr={oldStr}
        newStr={newStr}
        filePath={filePath}
        originalFile={context?.childOriginalFile}
        alwaysVisible={isEdit || isWrite}
        context={context}
      />
    )
  },
}

/** Set of tool names whose results should be rendered as preformatted text. */
const PRE_TEXT_TOOLS = new Set(['Bash', 'Grep', 'Glob', 'Read', 'TaskOutput'])

/** Extract error text from <tool_use_error> tags in tool result content. */
function extractToolUseError(content: string): string | null {
  const match = content.match(/<tool_use_error>([\s\S]*?)<\/tool_use_error>/)
  return match ? match[1].trim() : null
}

/** Reusable file-path list used by Grep/Glob result views. */
function FileListView(props: {
  filenames: string[]
  context?: RenderContext
}): JSX.Element {
  return (
    <ul class={toolFileList}>
      <For each={props.filenames}>
        {f => (
          <li class={toolInputPath}>
            {relativizePath(f, props.context?.workingDir, props.context?.homeDir)}
          </li>
        )}
      </For>
    </ul>
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

  return (
    <div class={toolMessage}>
      <Show
        when={hasResult()}
        fallback={<div class={toolResultContentPre}>{props.fallbackContent || 'No matches found'}</div>}
      >
        <Show when={props.filenames.length > 0}>
          <FileListView filenames={props.filenames} context={props.context} />
        </Show>
        <Show when={props.content}>
          <div class={toolResultContentPre}>{props.content}</div>
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
  return (
    <div class={toolMessage}>
      <Show
        when={props.filenames.length > 0}
        fallback={<div class={toolResultContentPre}>{props.fallbackContent || 'No files found'}</div>}
      >
        <FileListView filenames={props.filenames} context={props.context} />
      </Show>
    </div>
  )
}

/** Render Read tool results with syntax highlighting, or fall back to plain pre text. */
function renderReadOrPre(
  toolName: string,
  resultContent: string,
  context?: RenderContext,
): JSX.Element {
  if (toolName === 'Read') {
    const parsed = parseCatNContent(resultContent)
    if (parsed) {
      const filePath = context?.parentToolInput?.file_path as string | undefined
      return <ReadResultView lines={parsed} filePath={filePath} />
    }
  }
  return <div class={toolResultContentPre}>{resultContent}</div>
}

/** Inner component for tool_result messages with structuredPatch — owns local diff view state. */
function ToolResultMessage(props: {
  toolName: string
  resultContent: string
  isPreText: boolean
  webFetchPrompt: string
  structuredPatch: StructuredPatchHunk[] | null
  oldStr: string
  newStr: string
  filePath: string
  context?: RenderContext
}): JSX.Element {
  const { diffView, toggleDiffView } = useDiffViewToggle(() => props.context?.diffView)
  const hasPatch = () => !!props.structuredPatch && props.structuredPatch.length > 0
  const hasFallbackDiff = () => props.oldStr !== '' && props.newStr !== '' && props.oldStr !== props.newStr
  const hasDiff = () => hasPatch() || hasFallbackDiff()
  const errorText = () => extractToolUseError(props.resultContent)

  return (
    <div class={toolMessage}>
      {props.webFetchPrompt && (
        <div class={toolResultPrompt}>
          {'Prompt: '}
          {props.webFetchPrompt}
        </div>
      )}
      <Show
        when={!errorText()}
        fallback={<div class={toolResultError}>{errorText()}</div>}
      >
        <Show
          when={hasDiff()}
          fallback={
            props.isPreText
              ? ((props.toolName === 'Bash' || props.toolName === 'TaskOutput') && containsAnsi(props.resultContent))
                  /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderAnsi, not user input */
                  ? <div class={toolResultContentAnsi} innerHTML={renderAnsi(props.resultContent)} />
                  : renderReadOrPre(props.toolName, props.resultContent, props.context)
              /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */
              : <div class={toolResultContent} innerHTML={renderMarkdown(props.resultContent)} />
          }
        >
          <div class={toolUseHeader}>
            <Show when={props.filePath}>
              <span class={toolInputPath}>{relativizePath(props.filePath, props.context?.workingDir, props.context?.homeDir)}</span>
            </Show>
            <div class={toolHeaderActions}>
              <IconButton
                icon={diffView() === 'unified' ? Columns2 : Rows2}
                size="sm"
                onClick={() => toggleDiffView()}
                title={diffView() === 'unified' ? 'Switch to split view' : 'Switch to unified view'}
              />
            </div>
          </div>
          <DiffView
            hunks={hasPatch() ? props.structuredPatch! : rawDiffToHunks(props.oldStr, props.newStr)}
            view={diffView()}
            filePath={props.filePath}
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

    // Extract tool name from tool_use_result, or fall back to parent context
    const toolUseResult = parsed.tool_use_result as Record<string, unknown> | undefined
    const toolName = String(toolUseResult?.tool_name || context?.parentToolName || 'Result')

    // Determine whether this tool's output should be preformatted text
    const isPreText = PRE_TEXT_TOOLS.has(toolName)

    // For WebFetch, extract the prompt from parent tool input
    const webFetchPrompt = toolName === 'WebFetch' && context?.parentToolInput
      ? String(context.parentToolInput.prompt || '')
      : ''

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

    // Hide redundant result content: Edit/Write success messages when the
    // parent already shows the diff, and TodoWrite boilerplate messages.
    const hideContent = parentShowsDiff || toolName === 'TodoWrite'

    // Grep: render structured result view when tool_use_result has data.
    if (toolName === 'Grep' && toolUseResult) {
      const numFiles = typeof toolUseResult.numFiles === 'number' ? toolUseResult.numFiles : 0
      const numLines = typeof toolUseResult.numLines === 'number' ? toolUseResult.numLines : 0
      const filenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames as string[] : []
      const grepContent = typeof toolUseResult.content === 'string' ? toolUseResult.content : ''
      return (
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

    // Glob: render structured result view when tool_use_result has filenames.
    if (toolName === 'Glob' && toolUseResult) {
      const filenames = Array.isArray(toolUseResult.filenames) ? toolUseResult.filenames as string[] : []
      return (
        <GlobResultView
          filenames={filenames}
          fallbackContent={resultContent}
          context={context}
        />
      )
    }

    return (
      <ToolResultMessage
        toolName={toolName}
        resultContent={hideContent ? '' : resultContent}
        isPreText={isPreText}
        webFetchPrompt={webFetchPrompt}
        structuredPatch={structuredPatch}
        oldStr={oldStr}
        newStr={newStr}
        filePath={filePath}
        context={context}
      />
    )
  },
}
