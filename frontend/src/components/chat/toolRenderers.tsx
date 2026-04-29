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
import { createMemo, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { Tooltip } from '~/components/common/Tooltip'
import { escapeHtml } from '~/lib/renderAnsi'
import { shikiHighlighter } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { getToolResultExpanded } from './messageRenderers'
import { RelativeTime } from './RelativeTime'
import { CollapsibleContent } from './results/CollapsibleContent'
import { CommandResultBody } from './results/commandResult'
import { FileEditDiffBody, fileEditHasDiff } from './results/fileEditDiff'
import { parseCatNContent, ReadResultView } from './results/ReadResultView'
import { useCollapsedLines } from './results/useCollapsedLines'
import {
  controlResponseTag,
  toolBodyContent,
  toolHeaderActions,
  toolHeaderTimestamp,
  toolInputText,
  toolMessage,
  toolResultCollapsed,
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
  headerActions?: ToolHeaderActionsCallerProps
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
            caller={actions()}
            layout={{
              inline: true,
              createdAt: props.context?.createdAt,
              expanded: expanded(),
              onToggleExpand: props.onToggleExpand,
              expandLabel: props.expandLabel,
              onCopyJson: props.context?.onCopyJson,
              jsonCopied: props.context?.jsonCopied?.() ?? false,
              hasDiff: props.hasDiff,
              diffView: props.diffView,
              onToggleDiffView: props.onDiffViewChange ? () => props.onDiffViewChange!(props.diffView === 'unified' ? 'split' : 'unified') : undefined,
            }}
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
 * Caller-controlled buttons forwarded into `ToolHeaderActions`. These are
 * the subset of actions whose source is the *renderer* (e.g. an Edit tool's
 * "Copy diff", a markdown tool's "Copy markdown", a reply quote callback).
 *
 * `ToolUseLayout` re-exposes this bag verbatim as `headerActions=`.
 */
export interface ToolHeaderActionsCallerProps {
  onCopyContent?: () => void
  contentCopied?: boolean
  copyContentLabel?: string
  onReply?: () => void
  onCopyMarkdown?: () => void
  markdownCopied?: boolean
}

/**
 * Layout-controlled state forwarded into `ToolHeaderActions`. These come
 * from the wrapping layout/bubble (timestamp, expand state, JSON copy,
 * diff-view toggle), not the per-renderer caller.
 */
export interface ToolHeaderActionsLayoutProps {
  createdAt?: string
  expanded?: boolean
  onToggleExpand?: () => void
  expandLabel?: string
  onCopyJson?: () => void
  jsonCopied?: boolean
  hasDiff?: boolean
  diffView?: DiffViewPreference
  onToggleDiffView?: () => void
  /** When true, use inline tool header order; when false (default), use bubble order. */
  inline?: boolean
}

/** Actions area in tool header: Reply + Raw JSON copy + diff toggle + expand/collapse, all with tooltips. */
export function ToolHeaderActions(props: {
  caller?: ToolHeaderActionsCallerProps
  layout?: ToolHeaderActionsLayoutProps
}): JSX.Element {
  const caller = () => props.caller
  const layout = () => props.layout

  const replyButton = (
    <Show when={caller()?.onReply}>
      <IconButton
        icon={Quote}
        size="sm"
        data-testid="message-quote"
        onClick={() => caller()?.onReply?.()}
        title="Quote"
      />
    </Show>
  )
  const timestampEl = (
    <Show when={layout()?.createdAt}>
      <RelativeTime
        timestamp={layout()!.createdAt!}
        class={toolHeaderTimestamp}
      />
    </Show>
  )
  const copyJsonButton = (
    <Show when={layout()?.onCopyJson}>
      <IconButton
        icon={layout()?.jsonCopied ? Check : Braces}
        size="sm"
        data-testid="message-copy-json"
        onClick={() => layout()?.onCopyJson?.()}
        title={layout()?.jsonCopied ? 'Copied' : 'Copy Raw JSON'}
      />
    </Show>
  )
  const copyMarkdownButton = (
    <Show when={caller()?.onCopyMarkdown}>
      <IconButton
        icon={caller()?.markdownCopied ? Check : Copy}
        size="sm"
        data-testid="message-copy-markdown"
        onClick={() => caller()?.onCopyMarkdown?.()}
        title={caller()?.markdownCopied ? 'Copied' : 'Copy Markdown'}
      />
    </Show>
  )
  const copyContentButton = (
    <Show when={caller()?.onCopyContent}>
      <IconButton
        icon={caller()?.contentCopied ? Check : Copy}
        size="sm"
        onClick={() => caller()?.onCopyContent?.()}
        title={caller()?.contentCopied ? 'Copied' : (caller()?.copyContentLabel || 'Copy')}
      />
    </Show>
  )

  return (
    <div class={toolHeaderActions} data-testid="message-toolbar">
      {layout()?.inline
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
      <Show when={layout()?.hasDiff && layout()?.onToggleDiffView}>
        <IconButton
          icon={layout()?.diffView === 'unified' ? Columns2 : Rows2}
          size="sm"
          onClick={() => layout()!.onToggleDiffView!()}
          title={layout()?.diffView === 'unified' ? 'Switch to split view' : 'Switch to unified view'}
        />
      </Show>
      <Show when={layout()?.onToggleExpand}>
        <IconButton
          icon={layout()?.expanded ? FoldVertical : UnfoldVertical}
          size="sm"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            layout()!.onToggleExpand!()
          }}
          title={layout()?.expanded ? 'Collapse' : (layout()?.expandLabel || 'Expand')}
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

export function renderJsonHighlight(code: string): string {
  try {
    return shikiHighlighter.codeToHtml(code, {
      lang: 'json',
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
  const errorText = createMemo(() => extractToolUseError(props.resultContent))

  // Collapsible via expand/collapse button in MessageBubble toolbar.
  const isBashLike = () => props.displayKind === 'bash' || props.displayKind === undefined
  const normalizedResultContent = createMemo(() => isBashLike() ? stripLeadingBlankLines(props.resultContent) : props.resultContent)
  const expanded = () => getToolResultExpanded(props.context)
  const { display: displayContent, isCollapsed } = useCollapsedLines({ text: normalizedResultContent, expanded })

  const statusIcon = () => props.isError ? CircleAlert : Check

  // Always surface a status header when the tool failed, even for non-bash
  // display kinds — otherwise an Edit/Write rejection silently looks like a
  // success. For non-error results we keep the bash-only behavior so plain
  // tool results don't grow a redundant "Success" line.
  const showStatusHeader = () => props.isError === true || (isBashLike() && props.isError !== undefined)
  return (
    <Show
      when={!props.commandResult}
      fallback={<CommandResultBody source={props.commandResult!} context={props.context} />}
    >
      <div class={toolMessage} data-tool-message>
        <Show when={showStatusHeader()}>
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
          when={!errorText() && props.isError !== true}
          fallback={<div class={toolResultError}>{errorText() ?? props.resultContent}</div>}
        >
          <Show
            when={renderableDiff()}
            fallback={
              props.displayKind === 'read'
                ? renderReadOrPre(props.resultContent, props.readFilePath, !expanded())
                : (
                    <CollapsibleContent
                      kind={props.displayKind === 'markdown' ? 'markdown-tool-result' : 'ansi-or-pre'}
                      text={normalizedResultContent()}
                      display={displayContent()}
                      isCollapsed={isCollapsed()}
                    />
                  )
            }
          >
            {src => <FileEditDiffBody source={src()} view={diffView()} />}
          </Show>
        </Show>
      </div>
    </Show>
  )
}
