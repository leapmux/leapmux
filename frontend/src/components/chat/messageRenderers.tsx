import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { MessageRenderSources } from './messageContextResolver'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { ControlResponseDeriver, PersistedControlResponse } from './persistedControlResponse'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Braces from 'lucide-solid/icons/braces'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createMemo, createSignal, For, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { inlineFlex } from '~/styles/shared.css'
import { appendCompletionMarker, completionMarker, messageCompletionFromProto, parseAssembledMessage } from './assembledMessage'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { renderMarkdownForContext } from './markdownRendering'
import { attachmentItem, attachmentList, controlResponseLabel, controlResponseMessage, thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { CONTROL_RESPONSE_FEEDBACK_LEAD, resolveControlResponseDisplay } from './persistedControlResponse'
import { pluginFor } from './providers/registry'
import { ToolStatusHeader } from './results/ToolStatusHeader'
import { toolOutcomeNote } from './toolOutcome'
import { toolOutcomeLabel } from './toolOutcomeLabel'
import {
  toolInputText,
  toolResultContentPre,
  toolUseIcon,
} from './toolStyles.css'
import { MarkdownPlanLayout } from './widgets/MarkdownPlanLayout'
import { ToolUseLayout } from './widgets/ToolUseLayout'

export { markdownCacheNamespace, renderMarkdownForContext, shouldPauseSyntaxHighlighting } from './markdownRendering'

const logger = createLogger('messageRenderers')

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
  /** Original, supplemental, linked, and live data resolved for this row. */
  sources?: MessageRenderSources
  /** The enclosing renderer displays the retained tool completion. */
  completionHeader?: boolean
  /**
   * The agent sent no result for this tool call, and LeapMux says so in a note of
   * its own. A result renderer must draw NO body: an empty body reads as "the tool
   * returned nothing", which asserts something the agent never reported.
   */
  resultAbsent?: boolean
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
  /** Per-row/content-version pure render-derivation cache shared by visible + premeasure mounts. */
  renderCache?: MessageRenderCache
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Tool name or item type from span_type column (reliable, always set for span messages). */
  spanType?: string
  /** Current message span id. */
  spanId?: string
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: MessageUiKey) => boolean | undefined
  /** The message host supplies an outer toolbar with shared result actions. */
  hasOuterToolbar?: boolean
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: MessageUiKey, value: boolean) => void
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
  rowOffscreen?: () => boolean
  /** Open (or activate, or revive) a subagent's tab from its registry row. */
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  /**
   * Open an image this row rendered in its own tab.
   *
   * `index` addresses the image within its message -- the position the
   * provider's `toolResultImages` gives it. The handler is assembled where the
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
  onOpenImage?: (image: { index: number, filePath?: string, title?: string }) => void
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

export function useSharedExpandedState(
  getContext: () => RenderContext | undefined,
  key: MessageUiKey,
  // Defaults to the key's shared MESSAGE_UI_DEFAULTS entry (resolved against the
  // context's expandAgentThoughts pref); a renderer with a per-row default passes
  // its own thunk to override it.
  initial: () => boolean = () => messageUiDefault(key, { expandAgentThoughts: getContext()?.expandAgentThoughts }),
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
export function MarkdownText(props: { text: string, context?: RenderContext }): JSX.Element {
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
  // (THINKING / CODEX_REASONING follow expandAgentThoughts; PLAN_EXECUTION collapses)
  // via useSharedExpandedState, so renderer defaults stay centralized.
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)
  const body = (): JSX.Element => {
    if (props.renderBody)
      return props.renderBody()
    return <MarkdownText text={props.text} context={props.context} />
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
  return <ThinkingBubble text={props.text} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.THINKING} context={props.context} />
}

export function PlanExecutionMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.PLAN_EXECUTION} context={props.context} />
}

/**
 * Provider-neutral renderer for user messages persisted as
 * `{"content":"...", "attachments":[...]}` by the LeapMux service layer.
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
 * Render a persisted control-response row (issue #258). The provider plugin's
 * `controlResponseDisplay` owns the native-payload -> label/feedback derivation (one source of
 * truth with the scroll-rail preview); this is the shared markup for the two display kinds. A
 * feedback block renders the user's typed reason as markdown under the "Sent feedback:" lead; a
 * label renders line-broken plain text (a multi-question answer joins its lines with `\n`). Returns
 * null when `parsed` isn't a control-response envelope, so `renderMessageContent` falls through to
 * its raw-JSON safety net.
 */
export function renderControlResponseRow(
  cr: PersistedControlResponse,
  context: RenderContext | undefined,
  display: ControlResponseDeriver | undefined,
): JSX.Element {
  const d = resolveControlResponseDisplay(cr, display)
  if (d.kind === 'feedback') {
    return (
      <div class={controlResponseMessage}>
        <div>
          <div>{CONTROL_RESPONSE_FEEDBACK_LEAD}</div>
          <MarkdownText text={d.message} context={context} />
        </div>
      </div>
    )
  }
  return <div class={`${controlResponseMessage} ${controlResponseLabel}`} data-testid="control-response-text">{d.text}</div>
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
 * Returns an `UnrecognizedMessage` card when no plugin handles the message at all,
 * when JSON parsing fails, or when a renderer throws — the last-resort safety net.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
): JSX.Element {
  let renderFailed = false
  try {
    if (category?.kind === 'control_response')
      return renderControlResponseRow(category.response, context, pluginFor(agentProvider)?.controlResponseDisplay)

    const parsed = typeof parsedOrRawJson === 'string'
      ? JSON.parse(parsedOrRawJson)
      : parsedOrRawJson

    const assembled = parseAssembledMessage(parsed)
    if (assembled) {
      const text = appendCompletionMarker(assembled.text, messageCompletionFromProto(messageCompletion) ?? assembled.completion)
      switch (assembled.kind) {
        case 'reasoning':
          // ThinkingMessage, not a ThinkingBubble of its own: it already resolves the
          // expand key from the shared classification mapper, which is the key
          // ChatView premeasures the row under. A second spelling here drifted from
          // that once already, and Codex resolves to CODEX_REASONING, not THINKING.
          return <ThinkingMessage text={text} context={context} />
        case 'plan':
          return <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText={text} context={context} />
        case 'text':
          return <MarkdownText text={text} context={context} />
      }
    }

    // Dispatch strictly by the message's own provider -- no Claude fallback. An
    // unregistered/UNSPECIFIED provider yields no plugin, so we drop to the
    // raw-JSON span below rather than rendering another provider's bytes through
    // Claude's renderers (classifyMessage routes such messages to
    // `unsupported_provider`, which MessageBubble surfaces explicitly).
    const plugin = pluginFor(agentProvider)
    const completion = messageCompletionFromProto(messageCompletion) ?? messageCompletionFromProto(context?.sources?.current()?.completion)
    const toolCompletion = (category?.kind === 'tool_use' || category?.kind === 'tool_result')
      && (completion === 'interrupted' || completion === 'error')
    // The outcome note is LeapMux's own statement about a tool row, so it is drawn
    // here rather than by any plugin, and the plugin is told to draw no result body
    // of its own.
    const note = toolOutcomeNote(context?.sources?.current()?.messageMetadata)
    const contextOverrides: PropertyDescriptorMap = {}
    if (toolCompletion)
      contextOverrides.completionHeader = { value: true }
    if (note !== null)
      contextOverrides.resultAbsent = { value: true }
    const providerContext = Object.keys(contextOverrides).length === 0
      ? context
      : Object.create(context ?? null, contextOverrides) as RenderContext
    const result = plugin?.renderMessage?.(category ?? { kind: 'unknown' }, parsed, providerContext) ?? null
    if (result !== null) {
      // A tool row that is interrupted or failed already says so in its header, so
      // only the note rides inside that header -- the truncation marker would repeat
      // what the header states.
      const withNote = note === null
        ? result
        : (
            <>
              {result}
              <div role="note">{note}</div>
            </>
          )
      if (toolCompletion) {
        return (
          <ToolStatusHeader icon={CircleAlert} title={toolOutcomeLabel(completion === 'interrupted' ? 'interrupted' : 'failed')} dataToolMessage>
            {withNote}
          </ToolStatusHeader>
        )
      }
      const marker = completionMarker(completion)
      if (marker) {
        return (
          <>
            {withNote}
            <div role="note">{marker}</div>
          </>
        )
      }
      return withNote
    }

    // A user row is provider-neutral in the renderer layer for the same reason it
    // is in `classifyMessage`: LeapMux writes it, in its own flat
    // `{content, attachments?}` shape. Every plugin that names this kind already
    // draws it with UserContentMessage. A message whose provider metadata has not
    // loaded gets the same card here instead of the raw JSON span
    // below. Reached only when no plugin claimed the row above.
    if (category?.kind === 'user_content')
      return <UserContentMessage parsed={parsed} context={context} />
  }
  catch (err) {
    logger.warn('Failed to render message content:', err)
    renderFailed = true
  }
  // The row reached no renderer, so it says so and keeps its frame in a collapsed body.
  // It used to print the frame itself as a paragraph of text, which is the raw-JSON row
  // the shared standard forbids -- a live census caught one on GitHub Copilot.
  const fallback = <UnrecognizedMessage payload={parsedOrRawJson} renderFailed={renderFailed} context={context} />
  const marker = completionMarker(messageCompletionFromProto(messageCompletion))
  return marker
    ? (
        <>
          {fallback}
          <div role="note">{marker}</div>
        </>
      )
    : fallback
}
