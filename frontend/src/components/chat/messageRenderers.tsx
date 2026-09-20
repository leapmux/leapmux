import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageRenderSources } from './messageContextResolver'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { ControlResponseSummary } from './model/controlResponse'
import type { UserMessageAttachment } from './model/row'
import type { ImageRenderActions, MarkdownRenderContext, MessageUiRenderContext, SubagentNavigation, ToolProgressSource, ToolResultRenderContext } from './renderContext'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Braces from 'lucide-solid/icons/braces'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createMemo, createSignal, For, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { prettifyJson } from '~/lib/jsonFormat'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { renderMarkdownForContext } from './markdownRendering'
import { attachmentItem, attachmentList, controlResponseLabel, controlResponseMessage, thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { CONTROL_RESPONSE_FEEDBACK_LEAD } from './persistedControlResponse'
import {
  toolInputText,
  toolResultContentPre,
  toolUseIcon,
} from './toolStyles.css'
import { ToolUseLayout } from './widgets/ToolUseLayout'

export { markdownCacheNamespace, renderMarkdownForContext, shouldPauseSyntaxHighlighting } from './markdownRendering'

/**
 * Context passed to renderers from MessageBubble.
 *
 * Reactive UI state (`jsonCopied`, `diffView`) is exposed as getter functions
 * so the context object itself stays referentially stable across re-renders.
 * That lets the renderer functions called from MessageBubble skip re-running
 * on UI toggles — only the body components that actually read the getters
 * re-evaluate.
 *
 * Members marked `| undefined` below are assigned by reactive getters that
 * resolve through to undefined while the host is absent/loading; a getter
 * cannot omit a key, so `undefined` is the live "absent for now" state rather
 * than an invalid construction.
 */
export interface RenderContext extends ToolResultRenderContext {
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  /** Original, supplemental, linked, and live data resolved for this row. */
  sources?: MessageRenderSources
  /** The enclosing renderer displays the retained tool completion. */
  completionHeader?: boolean
  workingDir?: string | undefined
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string | undefined
  /** User's preferred diff view. */
  diffView?: () => DiffViewPreference
  /** Reply/quote callback — inserts quoted text into the editor. */
  onReply?: ((quotedText: string) => void) | undefined
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
  /** Per-row/content-version pure render-derivation cache shared by visible + premeasure mounts. */
  renderCache?: MessageRenderCache | undefined
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Tool name or item type from span_type column (reliable, always set for span messages). */
  spanType?: string | undefined
  /** Current message span id. */
  spanId?: string | undefined
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: ((key: MessageUiKey) => boolean | undefined) | undefined
  /** The message host supplies an outer toolbar with shared result actions. */
  hasOuterToolbar?: boolean
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: ((key: MessageUiKey, value: boolean) => void) | undefined
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
  /**
   * Whether this row currently sits OUTSIDE the near-viewport band (overscan-
   * only). Re-read at worker-dispatch time: renderers pass it as the
   * low-priority thunk for markdown/highlight jobs, so viewport rows' upgrades
   * preempt offscreen ones and an offscreen row upgrades automatically once
   * scrolled in (see createWorkerPriorityGate).
   */
  rowOffscreen?: (() => boolean) | undefined
  /** Open (or activate, or revive) a subagent's tab from its registry row. */
  onOpenSubagent?: ((item: BackgroundTaskItem) => void) | undefined
  /**
   * Resolving a subagent row and opening its transcript, without the registry
   * store. Assembled where the row's own navigation is in scope; the shared
   * result components read THIS rather than `sources.backgroundTask`.
   */
  subagents?: SubagentNavigation
  /**
   * Loading and opening the images this row drew, assembled once where the
   * message and the agent are both in scope. The image bodies read this rather
   * than the resolver's file-image channel.
   */
  images?: ImageRenderActions
  /**
   * The live output of a call that has not returned, for the row drawing its
   * tail. `ToolMessage` takes this as an explicit prop; the context member is
   * the assembly point its mount reads.
   */
  toolProgress?: ToolProgressSource
  /**
   * Open an image this row rendered in its own tab.
   *
   * `index` addresses the image within its message -- the position
   * `imagesForRow` gives it over the row's one call. The handler is assembled where the
   * message and the agent are both in scope (MessageBubble over ChatView), so
   * this context carries neither; a renderer only says WHICH image.
   *
   * `filePath` is present when the provider stated where the image came from.
   * The handler opens that file instead, which is the same picture at full
   * resolution and costs no new tab machinery.
   *
   * `title` is the row's own display name, supplied by whichever renderer
   * mounted the image -- it is the one layer that already computed a human
   * name for this row, so the tab reads the same as the row it came from
   * instead of restating the raw tool name. Omit it and the bubble falls back
   * to the span type.
   */
  onOpenImage?: ((image: { index: number, filePath?: string, title?: string }) => void) | undefined
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, context?: RenderContext) => JSX.Element | null
}

/**
 * Read the parent-driven tool-result-expanded flag from a render context.
 * Centralizes the `?.() ?? false` boilerplate every shared result body needs.
 */
export function getExpandedForKey(context: MessageUiRenderContext | undefined, key: MessageUiKey): boolean {
  const expandAgentThoughts = context?.expandAgentThoughts
  return context?.getMessageUiState?.(key)
    ?? messageUiDefault(key, expandAgentThoughts !== undefined ? { expandAgentThoughts } : {})
}

export function getToolResultExpanded(context: MessageUiRenderContext | undefined): boolean {
  return getExpandedForKey(context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
}

export function useSharedExpandedState(
  getContext: () => MessageUiRenderContext | undefined,
  key: MessageUiKey,
  // Defaults to the key's shared MESSAGE_UI_DEFAULTS entry (resolved against the
  // context's expandAgentThoughts pref); a renderer with a per-row default passes
  // its own thunk to override it.
  initial: () => boolean = () => {
    const expandAgentThoughts = getContext()?.expandAgentThoughts
    return messageUiDefault(key, expandAgentThoughts !== undefined ? { expandAgentThoughts } : {})
  },
): [() => boolean, (value: boolean | ((prev: boolean) => boolean)) => void] {
  const [localExpanded, setLocalExpanded] = createSignal<boolean | undefined>(undefined)
  const expanded = () => getContext()?.getMessageUiState?.(key) ?? localExpanded() ?? initial()
  const setExpanded = (value: boolean | ((prev: boolean) => boolean)) => {
    const ctx = getContext()
    const next = typeof value === 'function'
      ? (value as (prev: boolean) => boolean)(expanded())
      : value
    if (ctx?.setMessageUiState)
      ctx.setMessageUiState(key, next)
    else
      setLocalExpanded(next)
  }
  return [expanded, setExpanded]
}

/**
 * Render markdown text via the shared remark pipeline. The HTML is produced
 * via remark + sanitizer, never arbitrary user input. It is applied through
 * the parsed-fragment cache (~/lib/htmlFragmentCache) rather than an
 * `innerHTML` binding, so a re-mounting row clones the already-parsed
 * template instead of making the browser re-parse the same markup.
 */
export function MarkdownText(props: { text: string, context?: MarkdownRenderContext }): JSX.Element {
  const html = createMemo(() => renderMarkdownForContext(props.text, props.context))
  return <div class={markdownContent} ref={cachedInnerHtml(html)} />
}

type ThinkingBubbleProps = {
  icon: LucideIcon
  label: string
  stateKey: MessageUiKey
  context?: RenderContext
} & (
  | { text: string, renderBody?: never }
  | { text?: never, renderBody: () => JSX.Element }
)

/** Shared assistant thinking/reasoning bubble with chevron-controlled body. */
export function ThinkingBubble(props: ThinkingBubbleProps): JSX.Element {
  const stateKey = untrack(() => props.stateKey)
  // The default-expanded value comes from the stateKey's MESSAGE_UI_DEFAULTS entry
  // (THINKING follows expandAgentThoughts; PLAN_EXECUTION collapses) via
  // useSharedExpandedState, so renderer defaults stay centralized.
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)
  const body = (): JSX.Element => {
    if (props.renderBody)
      return props.renderBody()
    // The props union pairs `text` with an absent `renderBody`, so the body has
    // its text whenever this line runs.
    const text = props.text
    return text !== undefined
      ? <MarkdownText text={text} {...(props.context !== undefined ? { context: props.context } : {})} />
      : null
  }

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
          {body()}
        </div>
      </Show>
    </>
  )
}

export function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  // Key from the shared classification mapper (context.expandUiKey) so it matches
  // the estimator's pre-mount assumption; the literal is the context-less fallback.
  return <ThinkingBubble text={props.text} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.THINKING} {...(props.context !== undefined ? { context: props.context } : {})} />
}

export function PlanExecutionMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.PLAN_EXECUTION} {...(props.context !== undefined ? { context: props.context } : {})} />
}

/**
 * Provider-neutral renderer for user messages persisted as
 * `{"content":"...", "attachments":[...]}` by the LeapMux service layer.
 * Used by Claude, Codex, Pi, and every ACP-based provider
 * (OpenCode/Cursor/Goose/Kilo/Copilot/Reasonix) so no plugin has to reinvent
 * attachment + markdown rendering. Renders nothing when the parsed body has
 * no usable text or attachments.
 */
export function UserContentMessage(props: { text: string, attachments: UserMessageAttachment[], context?: RenderContext }): JSX.Element {
  const hasText = (): boolean => props.text.trim().length > 0
  const hasAttachments = (): boolean => props.attachments.length > 0
  const hasAny = (): boolean => hasText() || hasAttachments()

  return (
    <Show when={hasAny()}>
      <Show when={hasAttachments()}>
        <div class={attachmentList}>
          <For each={props.attachments}>
            {att => (
              <span class={attachmentItem}>
                <Icon
                  icon={att.mimeType?.startsWith('image/') ? FileImageIcon : FileIcon}
                  size="xs"
                />
                {att.filename ?? 'Unnamed file'}
              </span>
            )}
          </For>
        </div>
      </Show>
      <Show when={hasText()}>
        <MarkdownText text={props.text} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </Show>
  )
}

/**
 * Render a persisted control-response row (issue #258).
 *
 * The shared markup for the two display kinds, and nothing else: a feedback block
 * renders the user's typed reason as markdown under the "Sent feedback:" lead, and a
 * label renders line-broken plain text (a multi-question answer joins its lines with
 * `\n`).
 *
 * The native-payload -> label/feedback derivation happens in LAYER 1, where the
 * provider's `controlResponseDisplay` runs once for every reader of the row
 * (~/components/chat/rowExtraction.ts). This renderer therefore takes the display and
 * cannot reach a provider payload at all -- it used to run the derivation itself, so
 * the transcript row and the scroll-rail dot each dispatched through the plugin for
 * one answer, and each carried its own copy of the fallback.
 */
export function renderControlResponseRow(
  display: ControlResponseSummary,
  context: RenderContext | undefined,
): JSX.Element {
  if (display.kind === 'feedback') {
    return (
      <div class={controlResponseMessage}>
        <div>
          <div>{CONTROL_RESPONSE_FEEDBACK_LEAD}</div>
          <MarkdownText text={display.message} {...(context !== undefined ? { context } : {})} />
        </div>
      </div>
    )
  }
  return <div class={`${controlResponseMessage} ${controlResponseLabel}`} data-testid="control-response-text">{display.text}</div>
}

/**
 * The row that produced no display.
 *
 * Three things arrive here: a row no plugin claimed, a row a plugin claimed and then
 * drew nothing for -- a `settings_changed` notification with no changes in it -- and a
 * row whose renderer threw. The first two are one statement to the reader, because
 * LeapMux has nothing to show either way. The third is a defect in LeapMux, and calling
 * that "no display" would send the next reader hunting the provider instead.
 *
 * A live census found the first case: GitHub Copilot's `session.task_complete` reached
 * here, and the reader saw the whole JSON-RPC frame as a paragraph of text.
 *
 * The frame itself stays, because it is the only content this row has and hiding it
 * would lose whatever the provider did send. It starts COLLAPSED: an unrecognized row
 * is nearly always a frame the transcript has no use for, and the reader who wants it
 * is one click away. The toolbar's own Copy JSON action holds the same bytes.
 *
 * A plugin that can name its own unrecognized rows does not need this: `renderMessage`
 * already receives the `unknown` kind, so a provider renders its own card there and
 * never reaches this one. This card therefore reads NOTHING out of the payload, which
 * keeps every provider's wire shape inside that provider's plugin.
 *
 * It lives HERE rather than in `results/`, where the other row bodies live, because
 * `renderMessageContent` below must import it: every module under `results/` reaches
 * this one again through `toolRenderers`, and `src/test-support/noImportCycles.test.ts`
 * fails the suite for that cycle. `ToolUseLayout` comes from its own widget module for
 * the same reason -- the `toolRenderers` re-export would close the cycle.
 */
export function UnrecognizedMessage(props: {
  payload: unknown
  /** True when a renderer threw for this row, rather than no renderer claiming it. */
  renderFailed?: boolean
  context?: RenderContext
}): JSX.Element {
  const text = (): string => typeof props.payload === 'string' ? props.payload : prettifyJson(props.payload)
  const title = (): string => props.renderFailed
    ? 'LeapMux could not render this row'
    : 'LeapMux has no display for this row'
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.UNRECOGNIZED_ROW)

  // The layout gets NO context on purpose. It would draw a second Copy JSON action from
  // it, and the message host already supplies that one in the bubble's own toolbar --
  // two controls with one test id, and the reader with two buttons that do one thing.
  // The expand control stays, because the layout draws that from `onToggleExpand`.
  return (
    <ToolUseLayout
      icon={Braces}
      toolName="Unrecognized row"
      title={title()}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <div class={toolResultContentPre}>{text()}</div>
    </ToolUseLayout>
  )
}
