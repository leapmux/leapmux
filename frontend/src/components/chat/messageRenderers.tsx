import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import type { CommandStreamSegment } from '~/stores/chatTypes'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createMemo, createSignal, For, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { getCachedMarkdownHtml, renderMarkdown, renderMarkdownCachedOrPlain, renderMarkdownPlain } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { cachedRenderValueForString, getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'
import { attachmentItem, attachmentList, thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { pluginFor } from './providers/registry'
import {
  toolInputText,
  toolUseIcon,
} from './toolStyles.css'

const logger = createLogger('messageRenderers')

/**
 * Options for a per-message UI-state write, so the host can tell a user gesture
 * from a renderer-initiated write.
 */
export interface MessageUiWriteOptions {
  /**
   * The write was NOT initiated by a user gesture on the row (e.g. a stream-start
   * auto-expand effect). The host must not treat it as a toggle whose row should be
   * scroll-pinned: the reader's focus is wherever they are reading, so the default
   * viewport-midpoint anchor — which keeps THAT stationary — must win over a
   * row-top pin on the written row.
   */
  programmatic?: boolean
}

/**
 * Context passed to renderers from MessageBubble.
 *
 * Reactive UI state (`jsonCopied`, `diffView`) is exposed as getter functions
 * so the context object itself stays referentially stable across re-renders.
 * That lets the renderer functions called from MessageBubble skip re-running
 * on UI toggles — only the body components that actually read the getters
 * re-evaluate.
 */
export interface RenderContext {
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  /** O(1) live-todo lookup for this bubble's agent (resolves subjects for status-only TaskUpdate patches). */
  getTodoById?: (taskId: string) => TodoItem | undefined
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** User's preferred diff view. */
  diffView?: () => DiffViewPreference
  /** Reply/quote callback — inserts quoted text into the editor. */
  onReply?: (quotedText: string) => void
  /** Copy raw JSON to clipboard. */
  onCopyJson?: () => void
  /** Whether JSON was just copied (for feedback). */
  jsonCopied?: () => boolean
  /** Whether thinking/reasoning bubbles should start expanded by default. */
  expandAgentThoughts?: boolean
  /**
   * The per-message UI key for this row's EXPAND toggle (thinking/reasoning/plan/
   * agent-prompt bubble), resolved ONCE from the row's kind+provider via
   * `expandedUiKeyFor`. The thinking-style renderers read it instead of a hand-typed
   * literal, so they read the SAME key ChatView used for row state. Absent only
   * when a row is rendered without a MessageBubble context
   * (isolated tests/previews), where each renderer falls back to its own literal.
   */
  expandUiKey?: MessageUiKey
  /** Pre-parsed tool_use message for tool_result bubbles to inspect (cached by the store). */
  toolUseParsed?: ParsedMessageContent
  /** Pre-parsed tool_result message for tool_use bubbles to inspect (cached by the store). */
  toolResultParsed?: ParsedMessageContent
  /** Per-row/content-version pure render-derivation cache shared by visible + premeasure mounts. */
  renderCache?: MessageRenderCache
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Tool name or item type from span_type column (reliable, always set for span messages). */
  spanType?: string
  /** Current message span id. */
  spanId?: string
  /** Live streamed Codex span content for command, fileChange, and reasoning items. */
  commandStream?: () => CommandStreamSegment[] | undefined
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: MessageUiKey) => boolean | undefined
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: MessageUiKey, value: boolean, opts?: MessageUiWriteOptions) => void
  /**
   * Hidden premeasurement render pass. Renderers should keep layout-relevant
   * structure but skip non-geometry work such as timers, copy chrome, worker
   * dispatch, span-line drawing, and syntax highlighting.
   */
  premeasureMode?: boolean
  /**
   * Visible render pass is currently scroll-critical. Renderers should preserve
   * layout but skip Shiki/worker syntax jobs until this flips back to false.
   */
  syntaxHighlightingPaused?: () => boolean
  /**
   * A browser text selection is active inside this chat tree. Renderers must not
   * replace selected text nodes while this is true; doing so clears selection.
   */
  textSelectionActive?: () => boolean
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, context?: RenderContext) => JSX.Element | null
}

/**
 * Read the parent-driven tool-result-expanded flag from a render context.
 * Centralizes the `?.() ?? false` boilerplate every shared result body needs.
 */
export function getExpandedForKey(context: RenderContext | undefined, key: MessageUiKey): boolean {
  return context?.getMessageUiState?.(key)
    ?? messageUiDefault(key, { expandAgentThoughts: context?.expandAgentThoughts })
}

export function getToolResultExpanded(context: RenderContext | undefined): boolean {
  return getExpandedForKey(context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
}

export function shouldPauseSyntaxHighlighting(context: RenderContext | undefined): boolean {
  return context?.premeasureMode === true || context?.syntaxHighlightingPaused?.() === true || isTextSelectionActive(context)
}

function isTextSelectionActive(context: RenderContext | undefined): boolean {
  return context?.textSelectionActive?.() === true
}

function cachedHighlightedMarkdown(
  text: string,
  context: RenderContext | undefined,
): string | undefined {
  const rowCached = getCachedRenderValueForString<string>(context, 'markdown-html', text)
  if (rowCached !== undefined)
    return rowCached
  const sharedCached = getCachedMarkdownHtml(text)
  return sharedCached === undefined ? undefined : setCachedRenderValueForString(context, 'markdown-html', text, sharedCached)
}

function rememberDisplayedMarkdown(
  context: RenderContext | undefined,
  text: string,
  html: string,
): string {
  return setCachedRenderValueForString(context, 'markdown-displayed', text, html)
}

export function renderMarkdownForContext(text: string, context: RenderContext | undefined): string {
  if (context?.premeasureMode)
    return cachedRenderValueForString(context, 'markdown-plain', text, () => renderMarkdownPlain(text))
  if (isTextSelectionActive(context)) {
    const displayed = getCachedRenderValueForString<string>(context, 'markdown-displayed', text)
    if (displayed !== undefined)
      return displayed
    const highlighted = cachedHighlightedMarkdown(text, context)
    return rememberDisplayedMarkdown(
      context,
      text,
      highlighted ?? cachedRenderValueForString(context, 'markdown-plain', text, () => renderMarkdownPlain(text)),
    )
  }
  if (context?.syntaxHighlightingPaused?.()) {
    const highlighted = cachedHighlightedMarkdown(text, context)
    if (highlighted !== undefined)
      return rememberDisplayedMarkdown(context, text, highlighted)
    const html = renderMarkdownCachedOrPlain(text)
    const cached = getCachedMarkdownHtml(text)
    return rememberDisplayedMarkdown(
      context,
      text,
      cached === undefined ? html : setCachedRenderValueForString(context, 'markdown-html', text, cached),
    )
  }
  const rowCached = getCachedRenderValueForString<string>(context, 'markdown-html', text)
  if (rowCached !== undefined)
    return rememberDisplayedMarkdown(context, text, rowCached)
  const html = renderMarkdown(text)
  const cached = getCachedMarkdownHtml(text)
  return rememberDisplayedMarkdown(
    context,
    text,
    cached === undefined ? html : setCachedRenderValueForString(context, 'markdown-html', text, cached),
  )
}

export function useSharedExpandedState(
  getContext: () => RenderContext | undefined,
  key: MessageUiKey,
  // Defaults to the key's shared MESSAGE_UI_DEFAULTS entry (resolved against the
  // context's expandAgentThoughts pref); a renderer with a per-row default passes
  // its own thunk to override it.
  initial: () => boolean = () => messageUiDefault(key, { expandAgentThoughts: getContext()?.expandAgentThoughts }),
): [() => boolean, (value: boolean | ((prev: boolean) => boolean), opts?: MessageUiWriteOptions) => void] {
  const [localExpanded, setLocalExpanded] = createSignal<boolean | undefined>(undefined)
  const expanded = () => getContext()?.getMessageUiState?.(key) ?? localExpanded() ?? initial()
  const setExpanded = (value: boolean | ((prev: boolean) => boolean), opts?: MessageUiWriteOptions) => {
    const ctx = getContext()
    const next = typeof value === 'function'
      ? (value as (prev: boolean) => boolean)(expanded())
      : value
    if (ctx?.setMessageUiState)
      ctx.setMessageUiState(key, next, opts)
    else
      setLocalExpanded(next)
  }
  return [expanded, setExpanded]
}

/**
 * Render markdown text via the shared remark pipeline. The HTML is produced
 * via remark + sanitizer, never arbitrary user input — `solid/no-innerhtml`
 * is intentionally disabled at the call site here so every consumer doesn't
 * have to repeat the disable comment.
 */
export function MarkdownText(props: { text: string, context?: RenderContext }): JSX.Element {
  const html = createMemo(() => renderMarkdownForContext(props.text, props.context))
  // eslint-disable-next-line solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input
  return <div class={markdownContent} innerHTML={html()} />
}

/** Shared assistant thinking/reasoning bubble with chevron-controlled body. */
export function ThinkingBubble(props: {
  text: string
  icon: LucideIcon
  label: string
  stateKey: MessageUiKey
  context?: RenderContext
}): JSX.Element {
  const stateKey = untrack(() => props.stateKey)
  // The default-expanded value comes from the stateKey's MESSAGE_UI_DEFAULTS entry
  // (THINKING / CODEX_REASONING follow expandAgentThoughts; PLAN_EXECUTION collapses)
  // via useSharedExpandedState, so renderer defaults stay centralized.
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)

  return (
    <>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text={props.label} ariaLabel>
          <span class={inlineFlex}>
            <Icon icon={props.icon} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>{props.label}</span>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <MarkdownText text={props.text} context={props.context} />
        </div>
      </Show>
    </>
  )
}

export function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  // Key from the shared classification mapper (context.expandUiKey) so it matches
  // the estimator's pre-mount assumption; the literal is the context-less fallback.
  return <ThinkingBubble text={props.text} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.THINKING} context={props.context} />
}

export function PlanExecutionMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.PLAN_EXECUTION} context={props.context} />
}

/**
 * Provider-neutral renderer for user messages persisted as
 * `{"content":"...", "attachments":[...]}` by the Leapmux service layer.
 * Used by Claude, Codex, Pi, and every ACP-based provider
 * (OpenCode/Cursor/Goose/Kilo/Copilot/Reasonix) so no plugin has to reinvent
 * attachment + markdown rendering. Renders nothing when the parsed body has
 * no usable text or attachments.
 */
export function UserContentMessage(props: { parsed: unknown, context?: RenderContext }): JSX.Element {
  const parsed = (): Record<string, unknown> | null => {
    return isObject(props.parsed) ? props.parsed as Record<string, unknown> : null
  }
  const content = (): string => {
    const obj = parsed()
    return obj && typeof obj.content === 'string' ? obj.content as string : ''
  }
  const attachments = (): Array<{ filename?: string, mime_type?: string }> => {
    const obj = parsed()
    if (!obj || !Array.isArray(obj.attachments))
      return []
    return obj.attachments as Array<{ filename?: string, mime_type?: string }>
  }
  const hasText = (): boolean => content().trim().length > 0
  const hasAttachments = (): boolean => attachments().length > 0
  const hasAny = (): boolean => hasText() || hasAttachments()

  return (
    <Show when={hasAny()}>
      <Show when={hasAttachments()}>
        <div class={attachmentList}>
          <For each={attachments()}>
            {att => (
              <span class={attachmentItem}>
                <Icon
                  icon={att.mime_type?.startsWith('image/') ? FileImageIcon : FileIcon}
                  size="xs"
                />
                {att.filename ?? 'Unnamed file'}
              </span>
            )}
          </For>
        </div>
      </Show>
      <Show when={hasText()}>
        <MarkdownText text={content()} context={props.context} />
      </Show>
    </Show>
  )
}

/**
 * Render a message's content.
 *
 * All rendering goes through the message's own provider plugin's `renderMessage`.
 * The plugin is responsible for handling every kind it can render, including
 * `'unknown'` (where it runs its own type-detection chain on the parsed object).
 * Dispatch is strictly by `agentProvider` with no Claude fallback — an
 * UNSPECIFIED/unregistered provider yields no plugin (matching
 * `classifyMessage`, which routes such a message to `unsupported_provider`).
 *
 * Returns a raw-text `<span>` when no plugin handles the message at all
 * (or when JSON parsing fails) — the absolute last-resort safety net.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
): JSX.Element {
  try {
    const parsed = typeof parsedOrRawJson === 'string'
      ? JSON.parse(parsedOrRawJson)
      : parsedOrRawJson

    // Dispatch strictly by the message's own provider -- no Claude fallback. An
    // unregistered/UNSPECIFIED provider yields no plugin, so we drop to the
    // raw-JSON span below rather than rendering another provider's bytes through
    // Claude's renderers (classifyMessage routes such messages to
    // `unsupported_provider`, which MessageBubble surfaces explicitly).
    const plugin = pluginFor(agentProvider)
    const result = plugin?.renderMessage?.(category ?? { kind: 'unknown' }, parsed, context) ?? null
    if (result !== null)
      return result
  }
  catch (err) { logger.warn('Failed to render message content:', err) }
  return <span>{typeof parsedOrRawJson === 'string' ? parsedOrRawJson : JSON.stringify(parsedOrRawJson)}</span>
}
