/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from './diffUtils'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { BashInput, EditInput, GlobInput, GrepInput, ReadInput, TaskInput, WebFetchInput, WebSearchInput, WriteInput } from '~/types/toolMessages'
import { diffLines } from 'diff'
import ArrowUpLeft from 'lucide-solid/icons/arrow-up-left'
import Bot from 'lucide-solid/icons/bot'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import ChevronsRight from 'lucide-solid/icons/chevrons-right'
import Columns2 from 'lucide-solid/icons/columns-2'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FileText from 'lucide-solid/icons/file-text'
import FoldVertical from 'lucide-solid/icons/fold-vertical'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import Rows2 from 'lucide-solid/icons/rows-2'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import UnfoldVertical from 'lucide-solid/icons/unfold-vertical'
import Vote from 'lucide-solid/icons/vote'
import { createSignal, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { DiffView, rawDiffToHunks } from './diffUtils'
import { getAssistantContent, isObject, relativizePath } from './messageUtils'
import { parseCatNContent, ReadResultView } from './ReadResultView'
import { RelativeTime } from './RelativeTime'
import {
  controlResponseTag,
  toolHeaderActions,
  toolHeaderButtonHidden,
  toolHeaderTimestamp,
  toolInputCode,
  toolInputDetail,
  toolInputPath,
  toolInputStatAdded,
  toolInputStatRemoved,
  toolInputSubDetail,
  toolInputSubDetailExpanded,
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

/** Map tool name to its icon component. */
export function toolIcon(name: string, size: number): JSX.Element {
  switch (name) {
    case 'Bash': return <Terminal size={size} class={toolUseIcon} />
    case 'Read': return <FileText size={size} class={toolUseIcon} />
    case 'Write': return <FilePlus size={size} class={toolUseIcon} />
    case 'Edit': return <FilePen size={size} class={toolUseIcon} />
    case 'Grep': return <Search size={size} class={toolUseIcon} />
    case 'Glob': return <FolderSearch size={size} class={toolUseIcon} />
    case 'Task': return <Bot size={size} class={toolUseIcon} />
    case 'WebFetch': return <Globe size={size} class={toolUseIcon} />
    case 'WebSearch': return <Globe size={size} class={toolUseIcon} />
    case 'TodoWrite': return <ListTodo size={size} class={toolUseIcon} />
    case 'EnterPlanMode': return <TicketsPlane size={size} class={toolUseIcon} />
    case 'ExitPlanMode': return <PlaneTakeoff size={size} class={toolUseIcon} />
    case 'AskUserQuestion': return <Vote size={size} class={toolUseIcon} />
    default: return <ChevronsRight size={size} class={toolUseIcon} />
  }
}

/** Helper: format tool input for compact display (fallback for unknown tools) */
export function formatToolInput(input: unknown): string {
  if (input === null || input === undefined || JSON.stringify(input) === '{}') {
    return '()'
  }
  const json = JSON.stringify(input)
  if (json.length < 50) {
    return `(${json})`
  }
  return '({...})'
}

/** Render per-tool compact display for a tool_use block. */
export function renderToolDetail(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const cwd = context?.workingDir
  const homeDir = context?.homeDir

  switch (toolName) {
    case 'Bash': {
      const { description: desc, command: cmd } = input as BashInput
      if (!desc && !cmd)
        return null
      const descText = desc ? (desc.length > 100 ? `${desc.slice(0, 100)}…` : desc) : ''
      return <span class={toolInputDetail}>{descText || 'Run command'}</span>
    }
    case 'Read': {
      const { file_path: path, offset, limit } = input as ReadInput
      if (!path)
        return null
      const rangeStr = offset && limit
        ? ` (Line ${offset}–${offset + limit - 1})`
        : limit
          ? ` (Line 1–${limit})`
          : offset
            ? ` (Line ${offset}–)`
            : ''
      return (
        <>
          <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
          <span class={toolInputDetail}>{rangeStr}</span>
        </>
      )
    }
    case 'Write': {
      const { file_path: path, content } = input as WriteInput
      if (!path)
        return null
      const lineCount = content ? content.split('\n').length : 0
      const lineStr = lineCount > 0 ? ` (${lineCount} ${lineCount === 1 ? 'line' : 'lines'})` : ''
      return (
        <>
          <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
          <span class={toolInputDetail}>{lineStr}</span>
        </>
      )
    }
    case 'Edit': {
      const { file_path: path, old_string: oldStr, new_string: newStr } = input as EditInput
      if (!path)
        return null
      let added = 0
      let removed = 0
      if (oldStr && newStr && oldStr !== newStr) {
        const changes = diffLines(oldStr, newStr)
        for (const c of changes) {
          const count = c.value.replace(/\n$/, '').split('\n').length
          if (c.added)
            added += count
          else if (c.removed)
            removed += count
        }
      }
      const hasStats = added > 0 || removed > 0
      return (
        <>
          <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
          {hasStats && (
            <span class={toolInputDetail}>
              {' '}
              <span class={toolInputStatAdded}>
                {`+${added}`}
              </span>
              {' '}
              <span class={toolInputStatRemoved}>
                {`-${removed}`}
              </span>
            </span>
          )}
        </>
      )
    }
    case 'Grep': {
      const { pattern } = input as GrepInput
      return pattern
        ? <span class={toolInputCode}>{`"${pattern}"`}</span>
        : null
    }
    case 'Glob': {
      const { pattern, path } = input as GlobInput
      // Relativize pattern if it's an absolute path without glob wildcards
      const displayPattern = pattern && pattern.startsWith('/') && !pattern.includes('*')
        ? relativizePath(pattern, cwd, homeDir)
        : (pattern || '')
      return (
        <span class={toolInputCode}>
          {displayPattern}
          {path ? ` ${relativizePath(path, cwd, homeDir)}` : ''}
        </span>
      )
    }
    case 'Task': {
      const { description: desc, subagent_type: subagentType } = input as TaskInput
      return (
        <span class={toolInputDetail}>
          {desc || 'Task'}
          {subagentType ? ` (${subagentType})` : ''}
        </span>
      )
    }
    case 'WebFetch': {
      const { url } = input as WebFetchInput
      if (!url)
        return null
      return url.startsWith('https://')
        ? <span class={toolInputDetail}><a href={url} target="_blank" rel="noopener noreferrer nofollow">{url}</a></span>
        : <span class={toolInputDetail}>{url}</span>
    }
    case 'WebSearch': {
      const { query } = input as WebSearchInput
      return query ? <span class={toolInputDetail}>{query}</span> : null
    }
    default:
      return null
  }
}

/** Render an optional sub-detail line below the tool header (e.g. Bash command, Grep path). */
export function renderToolSubDetail(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const expanded = context?.threadExpanded
  const cls = expanded ? toolInputSubDetailExpanded : toolInputSubDetail
  switch (toolName) {
    case 'Bash': {
      const { command: cmd } = input as BashInput
      if (!cmd)
        return null
      if (expanded) {
        return <div class={cls}>{cmd}</div>
      }
      const firstLine = cmd.split('\n')[0]
      const truncated = firstLine.length > 120 ? `${firstLine.slice(0, 120)}…` : firstLine
      return <div class={cls}>{truncated}</div>
    }
    case 'Grep': {
      const { path } = input as GrepInput
      if (!path)
        return null
      return <div class={cls}>{relativizePath(path, context?.workingDir, context?.homeDir)}</div>
    }
    default:
      return null
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
}): JSX.Element {
  const timestamp = () => props.updatedAt || props.createdAt
  return (
    <div class={toolHeaderActions} data-testid="message-toolbar">
      <Show when={timestamp()}>
        <RelativeTime
          timestamp={timestamp()!}
          class={`${toolHeaderButtonHidden} ${toolHeaderTimestamp}`}
        />
      </Show>
      <Show when={props.onReply}>
        <IconButton
          icon={ArrowUpLeft}
          size="sm"
          class={toolHeaderButtonHidden}
          data-testid="message-reply"
          onClick={() => props.onReply?.()}
          title="Reply"
        />
      </Show>
      <Show when={props.onCopyJson}>
        <IconButton
          icon={props.jsonCopied ? Check : Braces}
          size="sm"
          class={toolHeaderButtonHidden}
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

/** Inner component for tool_use messages — owns local diff view state. */
function ToolUseMessage(props: {
  toolName: string
  detail: JSX.Element | null
  /** Optional secondary detail shown below the header line (e.g. Bash command). */
  subDetail?: JSX.Element | null
  fallbackDisplay: string | null
  hasDiff: boolean
  oldStr: string
  newStr: string
  filePath: string
  context?: RenderContext
}): JSX.Element {
  const [localDiffView, setLocalDiffView] = createSignal<DiffViewPreference | null>(null)
  const diffView = () => localDiffView() ?? props.context?.diffView ?? 'unified'
  const toggleDiffView = () => setLocalDiffView(diffView() === 'unified' ? 'split' : 'unified')

  const hasThread = () => (props.context?.threadChildCount ?? 0) > 0
  const hasActions = () => hasThread() || !!props.context?.onCopyJson || props.hasDiff

  return (
    <div class={toolMessage}>
      <div class={toolUseHeader}>
        <span class={inlineFlex} title={props.toolName}>
          {toolIcon(props.toolName, 16)}
        </span>
        <span>
          {props.detail ?? props.toolName}
          {props.fallbackDisplay && <>{props.fallbackDisplay}</>}
        </span>
        <ControlResponseTag response={props.context?.childControlResponse} />
        <Show when={hasActions()}>
          <ToolHeaderActions
            createdAt={props.context?.createdAt}
            updatedAt={props.context?.updatedAt}
            threadCount={props.context?.threadChildCount ?? 0}
            threadExpanded={props.context?.threadExpanded ?? false}
            onToggleThread={props.context?.onToggleThread ?? (() => {})}
            onCopyJson={props.context?.onCopyJson}
            jsonCopied={props.context?.jsonCopied}
            hasDiff={props.hasDiff}
            diffView={diffView()}
            onToggleDiffView={toggleDiffView}
          />
        </Show>
      </div>
      <Show when={props.subDetail}>
        {props.subDetail}
      </Show>
      <Show when={props.hasDiff}>
        <DiffView
          hunks={props.context?.childStructuredPatch ?? rawDiffToHunks(props.oldStr, props.newStr)}
          view={diffView()}
          filePath={props.filePath}
        />
      </Show>
    </div>
  )
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
    const subDetail = renderToolSubDetail(toolName, input, context)
    const fallbackDisplay = detail ? null : formatToolInput(toolData.input)

    // Edit tool: show diff view
    const isEdit = toolName === 'Edit'
    const editInput = isEdit ? input as EditInput : null
    const oldStr = editInput?.old_string ?? ''
    const newStr = editInput?.new_string ?? ''
    const hasDiff = isEdit && oldStr !== '' && newStr !== '' && oldStr !== newStr
    const filePath = editInput?.file_path ?? ''

    return (
      <ToolUseMessage
        toolName={toolName}
        detail={detail}
        subDetail={subDetail}
        fallbackDisplay={fallbackDisplay}
        hasDiff={hasDiff}
        oldStr={oldStr}
        newStr={newStr}
        filePath={filePath}
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
  const [localDiffView, setLocalDiffView] = createSignal<DiffViewPreference | null>(null)
  const diffView = () => localDiffView() ?? props.context?.diffView ?? 'unified'
  const toggleDiffView = () => setLocalDiffView(diffView() === 'unified' ? 'split' : 'unified')
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
              ? (props.toolName === 'Bash' && containsAnsi(props.resultContent))
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

    // Extract structuredPatch from tool_use_result for Edit/Write diffs
    const isEditOrWrite = toolName === 'Edit' || toolName === 'Write'
    const structuredPatch = isEditOrWrite && Array.isArray(toolUseResult?.structuredPatch)
      ? (toolUseResult!.structuredPatch as StructuredPatchHunk[])
      : null
    const filePath = isEditOrWrite ? String(toolUseResult?.filePath || '') : ''
    const oldStr = isEditOrWrite ? String(toolUseResult?.oldString || '') : ''
    const newStr = isEditOrWrite ? String(toolUseResult?.newString || '') : ''

    return (
      <ToolResultMessage
        toolName={toolName}
        resultContent={resultContent}
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
