import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { CommandResultSource } from './results/commandResult'
import type { FileEditDiffSource } from './results/fileEditDiff'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Columns2 from 'lucide-solid/icons/columns-2'
import Copy from 'lucide-solid/icons/copy'
import FoldVertical from 'lucide-solid/icons/fold-vertical'
import ListTodo from 'lucide-solid/icons/list-todo'
import Quote from 'lucide-solid/icons/quote'
import Rows2 from 'lucide-solid/icons/rows-2'
import UnfoldVertical from 'lucide-solid/icons/unfold-vertical'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { containsAnsi, escapeHtml, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown, shikiHighlighter } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { RelativeTime } from './RelativeTime'
import { CommandResultBody } from './results/commandResult'
import { FileEditDiffBody, fileEditHasDiff } from './results/fileEditDiff'
import { parseCatNContent, ReadResultView } from './results/ReadResultView'
import {
  controlResponseTag,
  toolBodyContent,
  toolHeaderActions,
  toolHeaderTimestamp,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultContentAnsi,
  toolResultContentPre,
  toolResultError,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'
import { spanColorKey } from './widgets/SpanLines'
import { spanLineColors } from './widgets/SpanLines.css'

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

/** Renders a "To-do list cleared" placeholder for empty todo/plan tool_use messages. */
export function EmptyTodoLayout(props: { toolName: string, context?: RenderContext }): JSX.Element {
  return <ToolUseLayout icon={ListTodo} toolName={props.toolName} title="To-do list cleared" context={props.context} />
}

/**
 * Shared layout for tool_use messages. Three content slots, ordered by
 * information density:
 *
 *  1. `title` — the header line, always visible. Identifies what the tool
 *     ran on (file path, command description, search pattern).
 *  2. `summary` — second line, also always visible when present. A brief
 *     preview that supplements the title (Bash command first line, Grep
 *     search path, etc.).
 *  3. `children` — "the details": the full expanded body. Hidden by default
 *     until the user clicks the expand toggle, OR shown unconditionally when
 *     `alwaysVisible` is set (e.g. TodoList where the list IS the content).
 *
 * Don't pass `summary` content as `children` or vice versa — the relationship
 * between summary (preview) and children (full) is what makes the
 * expand/collapse interaction read naturally.
 */
export function ToolUseLayout(props: {
  /** Lucide icon component (e.g. ListTodo, Vote, SquareTerminal). */
  icon: LucideIcon
  /** Tool name, used as the title attribute on the icon. */
  toolName: string
  /** Primary title shown in the header. String auto-wraps in toolInputText; JSX renders as-is. */
  title: string | JSX.Element
  /** Brief preview line below the header, always visible (when present). */
  summary?: JSX.Element
  /** The details — full body content, hidden until expanded (or always visible when `alwaysVisible` is set). */
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
  /**
   * Optional bag for the copy/reply/markdown buttons forwarded to
   * `ToolHeaderActions`. Layout-owned fields (timestamp, expanded, hasDiff,
   * etc.) come from `props.context` / `props.expanded` / `props.hasDiff` —
   * don't put them here.
   */
  headerActions?: ToolHeaderActionsForwardedProps
}): JSX.Element {
  const expanded = () => props.expanded ?? false
  const actions = () => props.headerActions
  const hasActions = () => !!props.onToggleExpand || !!props.context?.onCopyJson || !!props.hasDiff || !!actions()?.onCopyContent || !!actions()?.onCopyMarkdown || !!actions()?.onReply
  return (
    <div class={toolMessage} data-tool-message>
      <div class={toolUseHeader}>
        <Tooltip text={props.toolName} ariaLabel>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            <Icon icon={props.icon} size="md" />
          </span>
        </Tooltip>
        {typeof props.title === 'string'
          ? <span class={toolInputText}>{props.title}</span>
          : props.title}
        <Show when={hasActions()}>
          <ToolHeaderActions
            inline
            createdAt={props.context?.createdAt}
            expanded={expanded()}
            onToggleExpand={props.onToggleExpand}
            expandLabel={props.expandLabel}
            onCopyContent={actions()?.onCopyContent}
            contentCopied={actions()?.contentCopied}
            copyContentLabel={actions()?.copyContentLabel}
            onReply={actions()?.onReply}
            onCopyMarkdown={actions()?.onCopyMarkdown}
            markdownCopied={actions()?.markdownCopied}
            onCopyJson={props.context?.onCopyJson}
            jsonCopied={props.context?.jsonCopied?.() ?? false}
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

/**
 * The `ToolUseLayout`-forwarded subset of `ToolHeaderActions` props. Layout-
 * owned fields (timestamp, expanded, hasDiff, json copy) come from `context`
 * or sibling `ToolUseLayout` props, so they're excluded here.
 */
export interface ToolHeaderActionsForwardedProps {
  onCopyContent?: () => void
  contentCopied?: boolean
  copyContentLabel?: string
  onReply?: () => void
  onCopyMarkdown?: () => void
  markdownCopied?: boolean
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
  /** When true, use inline tool header order; when false (default), use bubble order. */
  inline?: boolean
}): JSX.Element {
  const timestamp = () => props.createdAt

  const replyButton = (
    <Show when={props.onReply}>
      <IconButton
        icon={Quote}
        size="sm"
        data-testid="message-quote"
        onClick={() => props.onReply?.()}
        title="Quote"
      />
    </Show>
  )
  const timestampEl = (
    <Show when={timestamp()}>
      <RelativeTime
        timestamp={timestamp()!}
        class={toolHeaderTimestamp}
      />
    </Show>
  )
  const copyJsonButton = (
    <Show when={props.onCopyJson}>
      <IconButton
        icon={props.jsonCopied ? Check : Braces}
        size="sm"
        data-testid="message-copy-json"
        onClick={() => props.onCopyJson?.()}
        title={props.jsonCopied ? 'Copied' : 'Copy Raw JSON'}
      />
    </Show>
  )
  const copyMarkdownButton = (
    <Show when={props.onCopyMarkdown}>
      <IconButton
        icon={props.markdownCopied ? Check : Copy}
        size="sm"
        data-testid="message-copy-markdown"
        onClick={() => props.onCopyMarkdown?.()}
        title={props.markdownCopied ? 'Copied' : 'Copy Markdown'}
      />
    </Show>
  )
  const copyContentButton = (
    <Show when={props.onCopyContent}>
      <IconButton
        icon={props.contentCopied ? Check : Copy}
        size="sm"
        onClick={() => props.onCopyContent?.()}
        title={props.contentCopied ? 'Copied' : (props.copyContentLabel || 'Copy')}
      />
    </Show>
  )

  return (
    <div class={toolHeaderActions} data-testid="message-toolbar">
      {props.inline
        ? (
            <>
              {timestampEl}
              {copyJsonButton}
              {copyMarkdownButton}
              {copyContentButton}
              {replyButton}
            </>
          )
        : (
            <>
              {replyButton}
              {timestampEl}
              {copyMarkdownButton}
              {copyJsonButton}
              {copyContentButton}
            </>
          )}
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

export function renderBashHighlight(code: string): string {
  try {
    return shikiHighlighter.codeToHtml(code, {
      lang: 'bash',
      themes: { light: 'github-light', dark: 'github-dark' },
      defaultColor: false,
    })
  }
  catch {
    return `<pre><code>${escapeHtml(code)}</code></pre>`
  }
}

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
 * How to render the body of a tool_result when no diff or commandResult is
 * provided:
 *
 *  - `'bash'` → strip leading blank lines, render ANSI/pre, and show a
 *    success/error header when `isError` is set. Used for any preformatted
 *    output that should look like a shell command result.
 *  - `'read'` → try to parse the content as cat-n format; render the
 *    syntax-highlighted ReadResultView when it parses, else plain pre.
 *  - `'pre'` → plain preformatted text.
 *  - `'markdown'` → render content as markdown.
 *
 * `undefined` is the catch-all when the body never reaches this branch (e.g.
 * the caller always provides `commandResult` or `diffSource`); behaves like
 * `'bash'` for header rendering when `isError` is set.
 */
export type ToolResultDisplayKind = 'bash' | 'read' | 'pre' | 'markdown'

/** Render Read tool results with syntax highlighting, or fall back to plain pre text. */
function renderReadOrPre(
  resultContent: string,
  readFilePath?: string,
  collapsed?: boolean,
): JSX.Element {
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
  return <div class={toolResultContentPre}>{resultContent}</div>
}

/**
 * Inner component for tool_result messages — renders an Edit/Write diff when
 *  `diffSource` carries one, otherwise falls back to text/Read/markdown via
 *  `displayKind`.
 */
export function ToolResultMessage(props: {
  resultContent: string
  /** How to render the body when neither `commandResult` nor `diffSource` is set. */
  displayKind?: ToolResultDisplayKind
  /** Pre-picked diff source (from `pickFileEditDiff`); null when no diff to render. */
  diffSource?: FileEditDiffSource | null
  /** File path for Read tool syntax highlighting (only used when displayKind='read'). */
  readFilePath?: string
  /** Whether the tool result is an error (from is_error field). */
  isError?: boolean
  /** Optional status detail shown inline with the Bash-like result header. */
  statusDetail?: string
  /**
   * Pre-built command-execution source. When set, the body delegates to
   * CommandResultBody for the canonical status header + output rendering.
   */
  commandResult?: CommandResultSource | null
  context?: RenderContext
}): JSX.Element {
  const diffView = () => props.context?.diffView?.() ?? 'unified'
  const renderableDiff = () => fileEditHasDiff(props.diffSource) ? props.diffSource : null
  const errorText = () => extractToolUseError(props.resultContent)

  // Collapsible via expand/collapse button in MessageBubble toolbar.
  const isBashLike = () => props.displayKind === 'bash' || props.displayKind === undefined
  const normalizedResultContent = () => isBashLike() ? stripLeadingBlankLines(props.resultContent) : props.resultContent
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const resultLines = () => normalizedResultContent().split('\n')
  const isCollapsed = () => {
    if (expanded())
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
    <Show
      when={!props.commandResult}
      fallback={<CommandResultBody source={props.commandResult!} context={props.context} />}
    >
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
            when={renderableDiff()}
            fallback={
              props.displayKind === 'markdown'
                /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderMarkdown, not user input */
                ? <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(displayContent())} />
                : props.displayKind === 'read'
                  ? renderReadOrPre(props.resultContent, props.readFilePath, !expanded())
                  : containsAnsi(normalizedResultContent())
                    /* eslint-disable-next-line solid/no-innerhtml -- HTML from renderAnsi, not user input */
                    ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayContent())} />
                    : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayContent()}</div>
            }
          >
            {src => <FileEditDiffBody source={src()} view={diffView()} />}
          </Show>
        </Show>
      </div>
    </Show>
  )
}
