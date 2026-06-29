import type { Component } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { MessageBubbleHost } from './MessageBubble'
import type { ChatScrollState, PaginationCallbacks } from './useChatScroll'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import type { CommandStreamSegment, SpanMessageRevision } from '~/stores/chatTypes'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createComputed, createEffect, createMemo, createSignal, For, Match, on, onCleanup, onMount, Show, Switch, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { Spinner } from '~/components/common/Spinner'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'
import { formatChatQuote } from '~/lib/quoteUtils'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { shallowEqualSets } from '~/lib/shallowEqual'
import { AgentStartupBanner } from './AgentStartupBanner'
import { createClassifiedEntryCache, heightKeyForEntry } from './chatEntryCache'
import { ChatHiddenPremeasure } from './chatHiddenPremeasure'
import { createMessageUiState } from './chatMessageUiState'
import { TOOL_USE_KIND_PREFIX } from './chatRowGeometry'
import * as styles from './ChatView.css'
import { computeOverscanPx, createViewportSizeObserver, measureSpaceToken, PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { MessageBubble } from './MessageBubble'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { assistantMessage } from './messageStyles.css'
import { ToolUseLayout } from './toolRenderers'
import { useChatScroll } from './useChatScroll'
import { sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'
import { bodySpanKey, SpanLines } from './widgets/SpanLines'
import { NO_SPAN_MARGIN } from './widgets/SpanLines.geometry'
import { ThinkingIndicator } from './widgets/ThinkingIndicator'

const SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS = 160
const SCROLL_PERF_SLOW_VIEWPORT_UPDATE_MS = 8
const SCROLL_PERF_SLOW_ROW_ATTACH_MS = 8
const SCROLL_PERF_ROW_BURST_COUNT = 8
const scrollPerfLog = createLogger('chatScrollPerf')

function classifiedEntryKind(entry: ClassifiedEntry | undefined): string | undefined {
  if (!entry)
    return undefined
  return entry.category.kind === 'tool_use'
    ? `${TOOL_USE_KIND_PREFIX}${entry.category.toolName}`
    : entry.category.kind
}

function addedRangeRowKinds(
  entries: readonly ClassifiedEntry[],
  previousStart: number,
  previousEnd: number,
  nextStart: number,
  nextEnd: number,
): Record<string, number> {
  const rowKinds: Record<string, number> = {}
  for (let index = nextStart; index < nextEnd; index++) {
    if (index >= previousStart && index < previousEnd)
      continue
    const kind = classifiedEntryKind(entries[index]) ?? 'unknown'
    rowKinds[kind] = (rowKinds[kind] ?? 0) + 1
  }
  return rowKinds
}

function heightKeyPart(value: string | undefined): string {
  return value === undefined ? '0:' : `${value.length}:${value}`
}

export function rowChromeHeightKey(error: string | undefined, pendingLabel: string | undefined): string {
  return `${heightKeyPart(error)}|${heightKeyPart(pendingLabel)}`
}

/** Imperative scroll API published by ChatView via `onScrollApiReady`. */
export interface ChatScrollApi {
  getScrollState: () => ChatScrollState | undefined
  forceScrollToBottom: () => void
  pageScroll: (direction: -1 | 1) => void
}

/**
 * The per-agent reactive lookups ChatView's renderers and entry cache consult by
 * spanId / message id. Bundled into one object (built once by the
 * host, TileRenderer) so the lookup cluster has a name and a single typed surface --
 * adding one no longer means a new prop plus three internal re-wiring sites. Every
 * member MUST read REACTIVELY (off the store) where noted, or an off-screen row freezes
 * at its first classification / measured-height cache key.
 */
export interface ChatMessageLookups {
  /** Look up the parsed tool_use message by spanId (for tool_use ↔ tool_result linking). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /**
   * The content version of the tool_use opener paired with a spanId (0 when none).
   * MUST read REACTIVELY (the store's getToolUseContentVersionBySpanId): a
   * tool_result renders from the opener's input, so an in-place opener body
   * change -- which bumps the OPENER's content version, not the result's -- must
   * re-classify the off-screen result and invalidate its cached DOM height. Folded
   * into the entry cache's freshness check and the per-row height key for
   * tool_result rows.
   */
  getToolUseContentVersionBySpanId?: (spanId: string) => number
  /** Full paired tool_use revision token (id + seq + content version). */
  getToolUseRevisionBySpanId?: (spanId: string) => SpanMessageRevision | undefined
  /** Symmetric counterpart: look up the parsed tool_result message by spanId. */
  getToolResultParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /**
   * The content version of the tool_result paired with a spanId (0 when none).
   * MUST read REACTIVELY: some tool_use renderers (Claude Task* rows) render
   * from the hidden result side, so result body changes must invalidate the
   * off-screen opener row's classification and measured DOM height.
   */
  getToolResultContentVersionBySpanId?: (spanId: string) => number
  /** Full paired tool_result revision token (id + seq + content version). */
  getToolResultRevisionBySpanId?: (spanId: string) => SpanMessageRevision | undefined
  /** Look up live Codex span stream segments by span id, for the bubble renderers. */
  getCommandStreamBySpanId?: (spanId: string) => CommandStreamSegment[]
  /**
   * Whether a span's command stream has renderable content to show. MUST read
   * REACTIVELY (e.g. the command-stream store's `hasRenderableContent`): the
   * classified-entry cache subscribes to it to flip a row hidden<->visible the
   * moment its span first has renderable stream content OR is cleared. A non-reactive
   * snapshot would type-check but freeze a Codex reasoning row on its first (hidden)
   * classification. Backed by a presence bit that flips only on those two events, so
   * subscribing doesn't re-classify per delta. NOT "actively streaming right now".
   */
  hasRenderableCommandStreamBySpanId?: (spanId: string) => boolean
  /**
   * The row's content version (the store's getMessageContentVersion), bumped when a
   * message's body is replaced in place under a stable id+seq. The classified-entry
   * cache and the height key fold it in so such an update re-classifies the row
   * and invalidates cached DOM height, which a same-seq proxy-preserving merge
   * would otherwise hide.
   */
  getMessageContentVersion?: (id: string) => number
  /** O(1) live-todo lookup for this view's agent (forwarded to renderers like the Claude Task card). */
  getTodoById?: (taskId: string) => TodoItem | undefined
}

/**
 * Pagination / windowing inputs: the boolean state signals PLUS the load/jump/trim
 * callbacks (PaginationCallbacks). Grouped into one prop so adding a windowing flag
 * touches a single typed surface instead of the flat prop list. The host builds this
 * once; the wiring block unpacks it into useChatScroll's flat ACCESSOR options (the
 * hook takes accessors where ChatView passes plain values, so the two layers can't
 * share the same object -- see PaginationCallbacks).
 */
export interface ChatPaginationProps extends PaginationCallbacks {
  /** Whether there are older messages available to fetch. */
  hasOlderMessages?: boolean
  /** Whether a fetch for older messages is in progress. */
  fetchingOlder?: boolean
  /** Whether newer messages exist beyond the in-memory window (scrolled away from the tail). */
  hasNewerMessages?: boolean
  /** Whether a fetch for newer messages / jump-to-latest is in progress. */
  fetchingNewer?: boolean
  /** Whether the window is within a page of the raw ceiling (can't grow); see chatStore.atWindowCeiling. */
  atWindowCeiling?: boolean
}

/**
 * Agent-lifecycle inputs: status + startup phase, plus the in-flight working / thinking
 * telemetry. Together they drive the empty-state startup banner and the thinking
 * indicator. Grouped so the lifecycle surface is one typed object on the prop list.
 */
export interface AgentLifecycleProps {
  /** Whether the agent is actively working (for showing the thinking indicator). */
  agentWorking?: boolean
  /**
   * Running estimate of the in-flight turn's thinking (reasoning) tokens, forwarded to
   * the thinking indicator. Broadcast-only telemetry, cleared at turn boundaries.
   */
  thinkingTokens?: number
  /**
   * Agent status. STARTING shows a loader with the provider name in the empty-state
   * area; STARTUP_FAILED shows the server error in --danger. The editor beneath remains
   * interactive during STARTING so the user can type ahead.
   */
  agentStatus?: AgentStatus
  /** Error text from the backend's AgentStatusChange.startup_error. */
  startupError?: string
  /** Phase label from AgentStatusChange.startup_message while STARTING (e.g. "Checking Git status…"). */
  startupMessage?: string
  /** Human-readable label for the agent provider (e.g. "Claude Code"). */
  providerLabel?: string
}

interface ChatViewProps {
  /**
   * Stable agent id. Forwarded to ThinkingIndicator so the random
   * spinner verb persists across re-mounts caused by layout-tree
   * restructures (tile split / make-grid / close-grid).
   */
  agentId?: string
  messages: AgentChatMessage[]
  streamingText: string
  /**
   * Whether this ChatView is the active tab in its tile. Forwarded to the
   * thinking indicator so it can suspend its compass simulation when the
   * user can't see it (every agent tab is mounted, including hidden ones).
   */
  tabActive?: boolean
  messageErrors?: Record<string, string>
  /** Per-message non-error sublabels (e.g. "Queued — agent is starting…"). */
  messagePendingLabels?: Record<string, string>
  onRetryMessage?: (messageId: string) => void
  onDeleteMessage?: (messageId: string) => void
  /** Workspace working directory for relativizing file paths in tool messages. */
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** Pagination / windowing state + callbacks (see ChatPaginationProps). */
  pagination?: ChatPaginationProps
  /** Saved scroll state for viewport restoration on tab switch. */
  savedViewportScroll?: ChatScrollState
  /** Called when saved scroll state should be cleared after restoration. */
  onClearSavedViewportScroll?: () => void
  /**
   * Receives the imperative scroll API once the chat viewport mounts.
   * The host (TileRenderer) needs this for tab-switch viewport save,
   * send-message scroll-to-bottom, and keyboard PageUp/PageDown.
   */
  onScrollApiReady?: (api: ChatScrollApi) => void
  /** Monotonic counter that increments on every addMessage (including thread merges). */
  messageVersion?: number
  /** Called when the user quotes selected text in a chat message. */
  onQuote?: (text: string) => void
  /** Called when the user clicks the reply button on an assistant message. */
  onReply?: (quotedText: string) => void
  /** When "plan", streaming text is rendered with plan styling. */
  streamingType?: string
  /**
   * The per-agent reactive lookups the bubble renderers, the classified-entry cache,
   * and the measured-height cache keys read by spanId / message id. Bundled into one stable
   * object (built once by the host) so adding a lookup touches a single typed surface
   * instead of the prop list plus three separate re-wiring sites.
   */
  lookups?: ChatMessageLookups
  /** Agent status / startup / thinking telemetry (see AgentLifecycleProps). */
  agentLifecycle?: AgentLifecycleProps
}

interface HeldStreamReplacement {
  tailId: string
  html: string
  type?: string
}

export const ChatView: Component<ChatViewProps> = (props) => {
  const prefs = usePreferences()

  // The virtualizer's live mounted-row set, assigned once `virt` is created below.
  // The UI-state cap reads it lazily (at toggle time, always after first render) to
  // protect on-screen rows from eviction -- mirrors how `flingSettle` is wired in
  // useChatScroll, so no forward reference to a not-yet-declared const.
  let mountedRowIds: ReadonlySet<string> = new Set()

  // Lifted per-message UI state (diff-view override + boolean flag map), keyed by
  // message id so a toggle survives <For> re-renders and a window trim. Owned by
  // createMessageUiState (its own tested unit); see that module for why it
  // deliberately outlives the windowed list and is cap-bounded instead of pruned.
  // The cap protects the currently-rendered rows so an on-screen row's choice is
  // never the eviction target.
  const { getLocalDiffView, setLocalDiffView, getMessageUiBool, setMessageUiBool, getUiVersion } = createMessageUiState({
    protectedIds: () => mountedRowIds,
  })
  const renderCacheStore = createMessageRenderCacheStore()
  const [textSelectionActive, setTextSelectionActive] = createSignal(false)

  // The AGENT-stable half of a MessageBubbleHost: the todo + span-parse lookups,
  // which are identical for every row of this agent and don't read the message.
  // Built once from props.lookups (itself built once by the host) rather than
  // re-bundled per row, so buildMessageHost below only assembles the genuinely
  // per-message bindings on top -- making the agent-scoped vs message-scoped split
  // explicit instead of a flat 8-field literal where the distinction is invisible.
  const hostLookups = createMemo(() => ({
    getTodoById: props.lookups?.getTodoById,
    getToolUseParsedBySpanId: props.lookups?.getToolUseParsedBySpanId,
    getToolResultParsedBySpanId: props.lookups?.getToolResultParsedBySpanId,
  }))

  // Throttle streaming text markdown rendering to animation frames to avoid
  // running the full remark+shiki pipeline on every streaming chunk.
  const [renderedStreamHtml, setRenderedStreamHtml] = createSignal('')
  const [heldStreamReplacement, setHeldStreamReplacement] = createSignal<HeldStreamReplacement | undefined>()
  let latestStreamingText = ''
  let latestStreamingType: string | undefined
  let latestRenderedStreamHtml = ''
  const streamCoalescer = createRafCoalescer<string>(text =>
    setRenderedStreamHtml(renderMarkdown(text, true)),
  )

  createEffect(() => {
    const html = renderedStreamHtml()
    if (props.streamingText && html)
      latestRenderedStreamHtml = html
  })

  createEffect(() => {
    const text = props.streamingText
    if (!text) {
      streamCoalescer.abort()
      setRenderedStreamHtml('')
      return
    }
    latestStreamingText = text
    latestStreamingType = props.streamingType
    latestRenderedStreamHtml = ''
    setHeldStreamReplacement(undefined)
    streamCoalescer.push(text)
  })

  onCleanup(() => streamCoalescer.abort())

  let contentRef: HTMLDivElement | undefined

  // Classify + cache the window's messages by id so <For> receives stable object
  // references for unchanged rows. createClassifiedEntryCache owns the cache, the
  // freshness rule (reuse only when seq AND command-stream presence are
  // unchanged), and the incremental prune.
  const entries = createClassifiedEntryCache({
    messages: () => props.messages,
    hasRenderableStreamBySpanId: spanId => props.lookups?.hasRenderableCommandStreamBySpanId?.(spanId) ?? false,
    // A tool_result's rendered content reads its paired tool_use; re-classify
    // the moment that opener is indexed so a late opener doesn't leave the row
    // frozen at its no-sibling shape.
    hasToolUseSiblingBySpanId: spanId => props.lookups?.getToolUseParsedBySpanId?.(spanId) !== undefined,
    // A tool_result reads its opener's content to render the diff; the opener is a
    // DIFFERENT message, so its in-place body change bumps the opener's version (not
    // the result's). Reading it here re-classifies the result row the moment its
    // opener changes, instead of leaving it frozen at the pre-change structure.
    toolUseSiblingContentVersionBySpanId: spanId => props.lookups?.getToolUseContentVersionBySpanId?.(spanId) ?? 0,
    toolUseSiblingRevisionBySpanId: spanId => props.lookups?.getToolUseRevisionBySpanId?.(spanId),
    // Some opener/tool_use rows render from the paired hidden result (Claude
    // TaskCreate/TaskUpdate/TaskGet). Track that side symmetrically so a result
    // edit invalidates the opener's cached classification/height.
    hasToolResultSiblingBySpanId: spanId => props.lookups?.getToolResultParsedBySpanId?.(spanId) !== undefined,
    toolResultSiblingContentVersionBySpanId: spanId => props.lookups?.getToolResultContentVersionBySpanId?.(spanId) ?? 0,
    toolResultSiblingRevisionBySpanId: spanId => props.lookups?.getToolResultRevisionBySpanId?.(spanId),
    // A same-seq in-place body replacement bumps this; reading it keeps the entry
    // cache from reusing the pre-update classification when the proxy/seq don't move.
    contentVersionById: id => props.lookups?.getMessageContentVersion?.(id) ?? 0,
    hasNewerMessages: () => !!props.pagination?.hasNewerMessages,
    showHiddenMessages: () => prefs.showHiddenMessages(),
  })
  const visibleEntries = entries.visibleEntries
  const hasVisibleEntries = entries.hasVisibleEntries

  // Inter-row gaps from the design tokens the non-virtual layout used, resolved
  // to pixels once mounted so the offset map keeps span-line bridges aligned (D6).
  const [gapSmallPx, setGapSmallPx] = createSignal(8)
  const [gapLargePx, setGapLargePx] = createSignal(20)
  onMount(() => {
    setGapSmallPx(measureSpaceToken('--space-2', 8))
    setGapLargePx(measureSpaceToken('--space-5', 20))
  })

  // Inner content width of the message list. One signal for all rows, bucketed
  // by the viewport observer so scrollbar/sub-pixel jitter doesn't storm the
  // measured-height cache keys.
  // We observe the SCROLL CONTAINER (messageList), NOT the inner content element.
  // "ResizeObserver loop completed with undelivered notifications" fires whenever
  // ANY observed element resizes DURING RO delivery -- the early-return in the
  // callback can't prevent it because the notification already happened. The
  // virtualizer's flush (itself an RO microtask) resizes the spacer on every row
  // measure, which grows the CONTENT element's height; observing that element
  // would hand our observer an undeliverable notification on every measure. The
  // scroll container's content-box height is fixed (content scrolls, it doesn't
  // grow), so it only resizes on a real viewport/sidebar change. Its content-box
  // width (padding excluded) IS the bubble wrap width. The write is still
  // deferred to a microtask as defense-in-depth.
  const [contentWidth, setContentWidth] = createSignal(0)
  // Message-list viewport height (the scroll container's content-box height),
  // tracked from the same observer. Drives the viewport-relative overscan below.
  const [viewportHeight, setViewportHeight] = createSignal(0)
  const viewportSizeObserver = createViewportSizeObserver({
    onWidth: setContentWidth,
    onHeight: setViewportHeight,
  })
  onCleanup(() => viewportSizeObserver.disconnect())
  const effectiveContentWidth = createMemo(() => contentWidth() > 0 ? contentWidth() : PRE_MEASURE_WIDTH_PX)

  // DOM heights are width- and UI-state-sensitive, so the measured-height cache key
  // folds the effective layout width and global height-affecting preferences into
  // every row. The width fallback must match hidden premeasure's fallback width: queued
  // unsettled rows can remain mounted while the pane reports width 0.
  const layoutEpochKey = createMemo(() =>
    JSON.stringify([
      effectiveContentWidth(),
      prefs.expandAgentThoughts() ? 1 : 0,
      prefs.diffView(),
      props.workingDir ?? '',
      props.homeDir ?? '',
    ]),
  )

  // Minimal per-row descriptors for the virtualizer. Unmeasured rows use only the
  // virtualizer's generic running-mean fallback until visible or hidden DOM
  // measurement commits a real height.
  const virtualItems = createMemo<VirtualItem[]>(
    () =>
      visibleEntries().map(e => ({
        id: e.msg.id,
        // Recorded onto a captured anchor so a trimmed-away row can be ordered against
        // the survivors for the nearest-survivor restore (scrollTopNearAnchor).
        seq: e.msg.seq,
        hasSpanLines: e.parsedSpanLines.length > 0,
        // The per-row measured-height cache key. heightKeyForEntry (chatEntryCache)
        // reads height-affecting freshness signals off the entry's own signature,
        // while layoutEpochKey covers width/global prefs. Reading getUiVersion HERE
        // subscribes this memo to the row's per-message UI toggle, so stale
        // premeasured heights are ignored the moment visible state changes.
        // DELIBERATELY EXCLUDED: live command-stream TEXT -- it grows on every
        // delta and is measured at the tail instead. The stream PRESENCE bit is
        // folded in (a classifier can change rendered structure from presence),
        // see EntryFreshness.
        heightKey: `${heightKeyForEntry(e, getUiVersion(e.msg.id))}|${layoutEpochKey()}|${rowChromeHeightKey(
          props.messageErrors?.[e.msg.id],
          props.messagePendingLabels?.[e.msg.id],
        )}`,
      })),
    [],
    // Suppress the geom rebuild + scroll re-pin when a recompute leaves the offset
    // map identical (same id/hasSpanLines/heightKey sequence) -- see sameVirtualItems.
    { equals: sameVirtualItems },
  )

  // Overscan scales with the live viewport height (clamped) -- see computeOverscanPx.
  const overscanPx = () => computeOverscanPx(viewportHeight())

  const virt = useChatVirtualizer({
    items: virtualItems,
    gapSmallPx,
    gapLargePx,
    overscanPx,
    shouldReportPerf: () => scrollPerfLog.isDebug(),
    onViewportUpdate: (stats) => {
      if (!scrollPerfLog.isDebug())
        return
      const shouldLog = stats.totalMs >= SCROLL_PERF_SLOW_VIEWPORT_UPDATE_MS
        || stats.addedRows >= SCROLL_PERF_ROW_BURST_COUNT
        || !!stats.tallRow
      if (!shouldLog)
        return
      const rowKinds = untrack(() =>
        addedRangeRowKinds(
          visibleEntries(),
          stats.previousStart,
          stats.previousEnd,
          stats.nextStart,
          stats.nextEnd,
        ),
      )
      const tallRowKind = stats.tallRow
        ? classifiedEntryKind(entries.getEntry(stats.tallRow.tallRowId)) ?? 'unknown'
        : undefined
      const payload = { ...stats, rowKinds }
      if (stats.tallRow)
        scrollPerfLog.debug('tall row range', { ...payload, tallRowKind })
      else
        scrollPerfLog.debug('viewport update', payload)
    },
    onRowAttachMeasure: (stats) => {
      if (!scrollPerfLog.isDebug())
        return
      const rowKind = classifiedEntryKind(entries.getEntry(stats.id)) ?? 'unknown'
      const isCommandExecution = rowKind === 'tool_use:commandExecution'
      if (!isCommandExecution && stats.totalMs < SCROLL_PERF_SLOW_ROW_ATTACH_MS)
        return
      scrollPerfLog.debug('visible row attach', { ...stats, rowKind })
    },
    onTallRowMeasure: (stats) => {
      if (!scrollPerfLog.isDebug())
        return
      const rowKind = classifiedEntryKind(entries.getEntry(stats.id)) ?? 'unknown'
      scrollPerfLog.debug('tall row measure', { ...stats, rowKind })
    },
  })
  // Point the UI-state cap's protect set at the virtualizer's live mounted rows
  // (a stable Set reference) now that `virt` exists.
  mountedRowIds = virt.mountedIds

  const renderCacheKeyForEntry = (entry: ClassifiedEntry): string =>
    `${entry.msg.id}|${heightKeyForEntry(entry, getUiVersion(entry.msg.id))}`

  createEffect(() => {
    renderCacheStore.prune(visibleEntries().map(renderCacheKeyForEntry))
  })

  const [syntaxHighlightingPaused, setSyntaxHighlightingPaused] = createSignal(false)

  /**
   * The per-row bindings a MessageBubble needs from ChatView, bundled into one
   * typed object: the agent-stable lookups (hostLookups) spread with the bindings
   * that genuinely vary per message -- the row's live command stream (keyed on its
   * spanId), its lifted diff-view / UI state (keyed on its id), and its
   * height-debug readout (keyed on its id, off `virt`). Built per row in the
   * <For>; the split documents exactly which bindings are message-scoped.
   * Defined after `virt` so getHeightDebug can read it without a
   * use-before-define.
   */
  const buildMessageHost = (entry: ClassifiedEntry): MessageBubbleHost => ({
    ...hostLookups(),
    commandStream: () => props.lookups?.getCommandStreamBySpanId?.(entry.msg.spanId),
    localDiffView: getLocalDiffView(entry.msg.id),
    onSetLocalDiffView: view => setLocalDiffView(entry.msg.id, view),
    getMessageUiState: key => getMessageUiBool(entry.msg.id, key),
    setMessageUiState: (key, value) => setMessageUiBool(entry.msg.id, key, value),
    getHeightDebug: () => virt.heightDebugOfId(entry.msg.id),
    renderCache: renderCacheStore.forRow(renderCacheKeyForEntry(entry)),
    syntaxHighlightingPaused,
    textSelectionActive,
  })

  // Hidden DOM premeasurement now mirrors the bounded rendered window: every
  // unmeasured row currently selected by the virtualizer gets a hidden render.
  // There is deliberately no idle delay, scroll cancellation, cost model, or batch
  // budget here; native virtualization already caps the number of mounted rows.
  const rangedPremeasureCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (contentWidth() <= 0)
      return []
    const all = visibleEntries()
    const items = virtualItems()
    const range = virt.range()
    const start = Math.max(0, Math.min(range.start, all.length, items.length))
    const end = Math.max(start, Math.min(range.end, all.length, items.length))
    const candidates: ChatDomPremeasureCandidate[] = []
    for (let index = start; index < end; index++) {
      const entry = all[index]
      const item = items[index]
      if (entry && item && !virt.hasMeasuredHeight(item.id))
        candidates.push({ entry, item })
    }
    return candidates
  })
  const removeIdFromSet = (ids: ReadonlySet<string>, id: string): ReadonlySet<string> => {
    if (!ids.has(id))
      return ids
    const next = new Set(ids)
    next.delete(id)
    return next
  }
  const removeIdFromMap = <V,>(items: ReadonlyMap<string, V>, id: string): ReadonlyMap<string, V> => {
    if (!items.has(id))
      return items
    const next = new Map(items)
    next.delete(id)
    return next
  }
  const [pendingPremeasureIds, setPendingPremeasureIds] = createSignal<ReadonlySet<string>>(new Set())
  const [collapsedPremeasureIds, setCollapsedPremeasureIds] = createSignal<ReadonlySet<string>>(new Set())
  const [unsettledPremeasureKeys, setUnsettledPremeasureKeys] = createSignal<ReadonlyMap<string, string | undefined>>(new Map())
  const virtualItemById = createMemo(() => {
    const result = new Map<string, VirtualItem>()
    for (const item of virtualItems())
      result.set(item.id, item)
    return result
  })
  const visibleEntryById = createMemo(() => {
    const result = new Map<string, ClassifiedEntry>()
    for (const entry of visibleEntries())
      result.set(entry.msg.id, entry)
    return result
  })
  const tailVisibleEntry = createMemo(() => {
    const all = visibleEntries()
    return all[all.length - 1]
  })
  // Hidden-premeasure collapse protects rows that have following content: a tall
  // unmeasured row can otherwise paint past its estimated slot and overlap the
  // next row. The live tail has no following message row to protect, so collapsing
  // it only creates a visible blank slot before its first measurement lands.
  const liveTailVisibleId = createMemo(() => (
    props.pagination?.hasNewerMessages ? undefined : tailVisibleEntry()?.msg.id
  ))
  createComputed(() => {
    const entries = visibleEntryById()
    const items = virtualItemById()
    const liveTailId = liveTailVisibleId()
    const unsettled = untrack(unsettledPremeasureKeys)
    const nextPending = new Set(untrack(pendingPremeasureIds))
    const nextCollapsed = new Set(untrack(collapsedPremeasureIds))
    const nextUnsettled = new Map(unsettled)
    for (const candidate of rangedPremeasureCandidates()) {
      nextPending.add(candidate.item.id)
      if (candidate.item.id !== liveTailId)
        nextCollapsed.add(candidate.item.id)
    }
    if (liveTailId !== undefined)
      nextCollapsed.delete(liveTailId)
    for (const id of [...nextPending]) {
      const item = items.get(id)
      if (!entries.has(id) || !item) {
        nextPending.delete(id)
        nextUnsettled.delete(id)
        continue
      }
      const unsettledKeyMatches = nextUnsettled.has(id) && nextUnsettled.get(id) === item.heightKey
      if ((virt.hasMeasuredHeight(id) || virt.hasPendingPremeasuredHeight(id)) && !unsettledKeyMatches)
        nextPending.delete(id)
    }
    for (const id of [...nextCollapsed]) {
      if (!entries.has(id) || !items.has(id) || virt.hasMeasuredHeight(id))
        nextCollapsed.delete(id)
    }
    for (const [id, heightKey] of [...nextUnsettled]) {
      const item = items.get(id)
      if (!entries.has(id) || !item || item.heightKey !== heightKey)
        nextUnsettled.delete(id)
    }

    const prevPending = untrack(pendingPremeasureIds)
    if (!shallowEqualSets(prevPending, nextPending))
      setPendingPremeasureIds(nextPending)
    const prevCollapsed = untrack(collapsedPremeasureIds)
    if (!shallowEqualSets(prevCollapsed, nextCollapsed))
      setCollapsedPremeasureIds(nextCollapsed)
    const prevUnsettled = untrack(unsettledPremeasureKeys)
    if (prevUnsettled.size !== nextUnsettled.size || [...prevUnsettled].some(([id, heightKey]) => nextUnsettled.get(id) !== heightKey))
      setUnsettledPremeasureKeys(nextUnsettled)
  })
  const premeasureCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    const ids = pendingPremeasureIds()
    if (ids.size === 0)
      return []
    const entries = visibleEntryById()
    const unsettled = unsettledPremeasureKeys()
    const candidates: ChatDomPremeasureCandidate[] = []
    for (const item of virtualItems()) {
      const unsettledKeyMatches = unsettled.has(item.id) && unsettled.get(item.id) === item.heightKey
      if (!ids.has(item.id) || (virt.hasMeasuredHeight(item.id) && !unsettledKeyMatches))
        continue
      const entry = entries.get(item.id)
      if (entry)
        candidates.push({ entry, item })
    }
    return candidates
  })
  const [streamReplacementTailId, setStreamReplacementTailId] = createSignal<string | undefined>()
  let streamingTailWasVisible = false
  let streamReplacementBaselineTailId: string | undefined
  let awaitingStreamReplacementTail = false
  const markStreamReplacementTail = (tailId: string | undefined): boolean => {
    if (tailId === undefined || tailId === streamReplacementBaselineTailId)
      return false
    awaitingStreamReplacementTail = false
    setStreamReplacementTailId(tailId)
    return true
  }
  // Keep the in-flow streaming bubble covering a persisted replacement row until that
  // row has real measured geometry; otherwise the indicator gap is anchored to an
  // estimated virtual spacer height while the visible bubble overflows it.
  const captureHeldStreamReplacement = (tailId: string | undefined): void => {
    if (tailId === undefined || latestStreamingText === '' || virt.hasMeasuredHeight(tailId))
      return
    setHeldStreamReplacement({
      tailId,
      html: latestRenderedStreamHtml || renderMarkdown(latestStreamingText, true),
      type: latestStreamingType,
    })
  }
  const streamingTailRender = createMemo(() => {
    if (props.streamingText) {
      return {
        html: renderedStreamHtml(),
        type: props.streamingType,
      }
    }
    const held = heldStreamReplacement()
    if (held === undefined)
      return undefined
    return {
      html: held.html,
      type: held.type,
    }
  })
  const isStreamReplacementCoveredByInFlowTail = (id: string): boolean =>
    streamReplacementTailId() === id && streamingTailRender() !== undefined
  createEffect(() => {
    const held = heldStreamReplacement()
    if (held === undefined)
      return
    if (streamReplacementTailId() !== held.tailId || props.pagination?.hasNewerMessages || virt.hasMeasuredHeight(held.tailId))
      setHeldStreamReplacement(undefined)
  })
  createEffect(() => {
    const streamingTailVisible = !!props.streamingText && !props.pagination?.hasNewerMessages
    const tailId = tailVisibleEntry()?.msg.id
    if (streamingTailVisible) {
      if (!streamingTailWasVisible) {
        streamingTailWasVisible = true
        streamReplacementBaselineTailId = tailId
        awaitingStreamReplacementTail = false
        setStreamReplacementTailId(undefined)
      }
      else {
        markStreamReplacementTail(tailId)
      }
      return
    }

    if (streamingTailWasVisible) {
      streamingTailWasVisible = false
      if (!markStreamReplacementTail(tailId)) {
        // The persisted assistant row can arrive after streaming clears, with
        // hidden lifecycle/meta rows in between. Keep one tail-change exemption
        // pending so that eventual visible row does not blink behind premeasure.
        awaitingStreamReplacementTail = true
        setStreamReplacementTailId(undefined)
      }
      else {
        captureHeldStreamReplacement(tailId)
      }
      return
    }

    if (awaitingStreamReplacementTail && markStreamReplacementTail(tailId)) {
      captureHeldStreamReplacement(tailId)
      return
    }

    if (streamReplacementTailId() !== undefined && streamReplacementTailId() !== tailId)
      setStreamReplacementTailId(undefined)
  })
  createComputed(() => {
    const collapsed = new Set(collapsedPremeasureIds())
    const liveTailId = liveTailVisibleId()
    const exemptTailId = streamReplacementTailId()
    if (liveTailId !== undefined)
      collapsed.delete(liveTailId)
    if (exemptTailId !== undefined)
      collapsed.delete(exemptTailId)
    virt.setCollapsedUntilMeasuredIds(collapsed)
  })

  let syntaxHighlightResumeTimer: ReturnType<typeof setTimeout> | undefined
  const pauseSyntaxHighlightingForScroll = () => {
    setSyntaxHighlightingPaused(true)
    if (syntaxHighlightResumeTimer !== undefined)
      clearTimeout(syntaxHighlightResumeTimer)
    syntaxHighlightResumeTimer = setTimeout(() => {
      syntaxHighlightResumeTimer = undefined
      setSyntaxHighlightingPaused(false)
    }, SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS)
  }

  onCleanup(() => {
    if (syntaxHighlightResumeTimer !== undefined)
      clearTimeout(syntaxHighlightResumeTimer)
  })

  const renderMessageBubble = (entry: ClassifiedEntry, opts: { premeasureMode?: boolean } = {}) => (
    <MessageBubble
      message={entry.msg}
      parsed={entry.parsed}
      category={entry.category}
      error={props.messageErrors?.[entry.msg.id]}
      pendingLabel={props.messagePendingLabels?.[entry.msg.id]}
      onRetry={() => props.onRetryMessage?.(entry.msg.id)}
      onDelete={() => props.onDeleteMessage?.(entry.msg.id)}
      workingDir={props.workingDir}
      homeDir={props.homeDir}
      onReply={props.onReply}
      host={buildMessageHost(entry)}
      premeasureMode={opts.premeasureMode}
    />
  )

  // The rendered window: only the rows in/near the viewport.
  const visibleSlice = createMemo(() => {
    const all = visibleEntries()
    const r = virt.range()
    return all.slice(r.start, r.end)
  })

  const scroll = useChatScroll({
    messages: () => props.messages,
    messageVersion: () => props.messageVersion,
    streamingText: () => props.streamingText,
    agentWorking: () => props.agentLifecycle?.agentWorking,
    agentStatus: () => props.agentLifecycle?.agentStatus,
    hasOlderMessages: () => props.pagination?.hasOlderMessages,
    fetchingOlder: () => props.pagination?.fetchingOlder,
    onLoadOlderMessages: () => props.pagination?.onLoadOlderMessages?.(),
    onTrimOldMessages: minKeep => props.pagination?.onTrimOldMessages?.(minKeep),
    hasNewerMessages: () => props.pagination?.hasNewerMessages,
    fetchingNewer: () => props.pagination?.fetchingNewer,
    atWindowCeiling: () => props.pagination?.atWindowCeiling,
    onLoadNewerMessages: () => props.pagination?.onLoadNewerMessages?.(),
    onJumpToLatest: () => props.pagination?.onJumpToLatest?.(),
    onJumpToOldest: () => props.pagination?.onJumpToOldest?.(),
    virtualizer: virt,
    savedViewportScroll: () => props.savedViewportScroll,
    onClearSavedViewportScroll: () => props.onClearSavedViewportScroll?.(),
  })

  const pauseThen = <Args extends unknown[]>(handler: (...args: Args) => void): ((...args: Args) => void) =>
    (...args: Args) => {
      pauseSyntaxHighlightingForScroll()
      handler(...args)
    }

  const scrollHandlers = {
    onScroll: pauseThen(scroll.handlers.onScroll),
    onWheel: pauseThen(scroll.handlers.onWheel),
    onKeyDown: (event: KeyboardEvent) => {
      if (event.key === 'ArrowDown' || event.key === 'PageDown' || event.key === 'End' || event.key === ' '
        || event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home') {
        pauseSyntaxHighlightingForScroll()
      }
      scroll.handlers.onKeyDown(event)
    },
    onTouchStart: pauseThen(scroll.handlers.onTouchStart),
    onTouchMove: pauseThen(scroll.handlers.onTouchMove),
    onTouchEnd: pauseThen(scroll.handlers.onTouchEnd),
    onTouchCancel: pauseThen(scroll.handlers.onTouchCancel),
    onPointerDown: pauseThen(scroll.handlers.onPointerDown),
    onPointerMove: (event: PointerEvent) => {
      scroll.handlers.onPointerMove(event)
    },
    onPointerUp: pauseThen(scroll.handlers.onPointerUp),
    onPointerCancel: pauseThen(scroll.handlers.onPointerCancel),
  }

  // Re-stick to the bottom after a TAIL SIBLING of the virtual spacer grows. These
  // siblings render outside any ResizeObserver here (the virtualizer measures only
  // rows; the scroll container's content-box is fixed), so their growth is invisible
  // to the auto-scroll signature. queueMicrotask defers past the DOM commit so
  // scrollHeight reflects the grown sibling; the re-stick is position-only and only
  // fires while pinned at the bottom. One home for the load-bearing deferred-re-stick
  // rule so a new tail sibling can't forget the queueMicrotask -- each one just wires
  // its growth trigger to `createEffect(on(trigger, restickAfterCommit))`.
  const restickAfterCommit = () => queueMicrotask(() => scroll.restickIfAtBottom())

  // Streaming markdown: renderedStreamHtml updates a frame after the streaming text
  // changes, AFTER the auto-scroll effect already read scrollHeight at the pre-render
  // size -- so re-stick once the markdown has actually rendered.
  createEffect(on(renderedStreamHtml, restickAfterCommit))

  // Startup banner (AgentStartupBanner phase labels + error text): a late phase-label
  // or error change does NOT move the auto-scroll signature (agentStatus stays
  // STARTING/STARTUP_FAILED across phases, and the banner text isn't in the signature).
  createEffect(on(() => [props.agentLifecycle?.startupMessage, props.agentLifecycle?.startupError], restickAfterCommit))

  // Thinking indicator: while agentWorking stays true and the indicator is already
  // shown, its thinking-token count climbs (a frequent reasoning-only-turn signal) and
  // can wrap the verb row to a taller line -- growth that does NOT move the auto-scroll
  // signature and that onExpandTick only catches during the expand/mount animation.
  createEffect(on(() => props.agentLifecycle?.thinkingTokens, restickAfterCommit))

  onMount(() => {
    props.onScrollApiReady?.({
      getScrollState: scroll.getScrollState,
      forceScrollToBottom: scroll.forceScrollToBottom,
      pageScroll: scroll.pageScroll,
    })
  })

  return (
    <div class={styles.container} data-testid="chat-container">
      <div class={styles.messageListWrapper}>
        <div
          ref={(el) => {
            scroll.attachListRef(el)
            viewportSizeObserver.observe(el)
          }}
          class={styles.messageList}
          data-chat-scroll-container="true"
          tabIndex={0}
          {...scrollHandlers}
        >
          {/*
            AgentStartupBanner is rendered in two places below: once in the
            empty-state fallback and once trailing the message list. They
            are NOT redundant — the outer <Show> only renders one branch at
            a time, so at most one banner is in the DOM for any given state.
          */}
          <Show
            when={hasVisibleEntries() || props.streamingText || props.agentLifecycle?.agentWorking
              || props.pagination?.hasOlderMessages || props.pagination?.hasNewerMessages
              || props.pagination?.fetchingOlder || props.pagination?.fetchingNewer}
            fallback={(
              <Switch fallback={<div class={styles.emptyChat}>Send a message to start</div>}>
                <Match when={props.agentLifecycle?.agentStatus === AgentStatus.STARTING || props.agentLifecycle?.agentStatus === AgentStatus.STARTUP_FAILED}>
                  <AgentStartupBanner
                    status={props.agentLifecycle?.agentStatus}
                    providerLabel={props.agentLifecycle?.providerLabel}
                    startupError={props.agentLifecycle?.startupError}
                    startupMessage={props.agentLifecycle?.startupMessage}
                    containerClass={styles.emptyChat}
                  />
                </Match>
              </Switch>
            )}
          >
            <div class={styles.messageListSpacer} />
            <SelectionQuotePopover
              class={styles.messageListSelectionRoot}
              containerRef={contentRef}
              onQuote={text => props.onQuote?.(formatChatQuote(text))}
              onSelectionActiveChange={setTextSelectionActive}
            >
              <div
                ref={(el) => {
                  contentRef = el
                }}
                class={styles.messageListContent}
              >
                {/*
                  Virtualized list: only rows in/near the viewport are mounted,
                  absolutely positioned by translateY inside a spacer sized to
                  the whole window's height (so the native scrollbar is correct).

                  Sizing the spacer to ONLY the loaded window's height is mechanism
                  #1 of the NO-SKIP INVARIANT (see FLING_OVERSCAN_LOOKAHEAD_MS in
                  useChatScroll): the browser clamps scrollTop to this height, so a
                  fast fling cannot scroll past the loaded edge into unloaded history
                  -- it stalls there until the buffer filler loads more, never skips.
                */}
                <div class={styles.virtualSpacer} style={{ height: `${virt.totalHeight()}px` }}>
                  <For each={visibleSlice()}>
                    {(entry, index) => {
                      const { msg, parsedSpanLines } = entry
                      // Offset is resolved by the row's own id, not by
                      // range().start + localIndex(): the id is the stable,
                      // unique key into the offset map (seq is 0n for every
                      // optimistic local), so it can't transiently disagree with
                      // the slice bounds during a scroll/measure flush.
                      const top = () => virt.offsetOfId(msg.id) ?? 0
                      const hideUntilMeasured = () => (
                        isStreamReplacementCoveredByInFlowTail(msg.id)
                        || (
                          !virt.hasMeasuredHeight(msg.id)
                          && collapsedPremeasureIds().has(msg.id)
                          && streamReplacementTailId() !== msg.id
                          && liveTailVisibleId() !== msg.id
                        )
                      )
                      const previousSpanLines = () => {
                        const previousIndex = virt.range().start + index() - 1
                        return visibleEntries()[previousIndex]?.parsedSpanLines ?? []
                      }
                      const previousBodySpanKey = () => {
                        const previousIndex = virt.range().start + index() - 1
                        const previous = visibleEntries()[previousIndex]
                        if (previous?.category.kind !== 'tool_use')
                          return undefined
                        return bodySpanKey(previous.msg.spanId, previous.msg.spanColor)
                      }
                      const bubble = renderMessageBubble(entry)

                      return (
                        <div
                          class={`${styles.virtualRow} ${styles.virtualRowAppear}`}
                          style={{
                            transform: `translateY(${top()}px)`,
                            // Absolute rows do not reserve flow height for siblings.
                            // Keep an actively premeasured unknown-height row invisible
                            // until its DOM height has committed. Otherwise a tall row
                            // can paint beyond its estimated slot and overlap rows that
                            // are already on screen. Rows with no active premeasure stay
                            // visible so a zero attach read cannot hide content forever.
                            visibility: hideUntilMeasured() ? 'hidden' : undefined,
                            opacity: hideUntilMeasured() ? '0' : '1',
                          }}
                          data-seq={msg.seq.toString()}
                          ref={(el) => {
                            virt.attachRow(msg.id, el)
                            onCleanup(() => virt.detachRow(el))
                          }}
                        >
                          <Show
                            when={parsedSpanLines.length > 0}
                            fallback={<div style={{ 'margin-left': `${NO_SPAN_MARGIN}px` }}>{bubble}</div>}
                          >
                            <div class={styles.messageRow}>
                              <SpanLines
                                lines={parsedSpanLines}
                                previousBodySpanKey={previousBodySpanKey()}
                                previousLines={previousSpanLines()}
                                spanOpener={!!msg.spanId}
                              />
                              <div class={styles.messageRowContent}>
                                {bubble}
                              </div>
                            </div>
                          </Show>
                        </div>
                      )
                    }}
                  </For>
                </div>
                {/*
                  Streaming text and the thinking indicator belong at the live
                  tail. While windowed away from the tail (hasNewerMessages) the
                  bottom of the in-memory list isn't the real bottom, so hide
                  them — the scroll-to-bottom button jumps back to the tail.
                */}
                <Show when={!props.pagination?.hasNewerMessages}>
                  <Show when={streamingTailRender()}>
                    {streamingTail => (
                      <Show
                        when={streamingTail().type === 'plan'}
                        fallback={(
                          <div class={assistantMessage}>
                            {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                            <div class={markdownContent} innerHTML={streamingTail().html} />
                          </div>
                        )}
                      >
                        <ToolUseLayout
                          icon={PlaneTakeoff}
                          toolName="Plan"
                          title="Proposed Plan"
                          alwaysVisible={true}
                          bordered={false}
                        >
                          <>
                            <hr />
                            {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                            <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={streamingTail().html} />
                          </>
                        </ToolUseLayout>
                      </Show>
                    )}
                  </Show>
                  <ThinkingIndicator
                    id={props.agentId}
                    visible={props.agentLifecycle?.agentWorking ?? false}
                    thinkingTokens={props.agentLifecycle?.thinkingTokens}
                    paused={props.tabActive === false}
                    onExpandTick={() => {
                      if (scroll.isAtBottomFresh())
                        scroll.jumpToBottom()
                    }}
                  />
                  {/*
                    The startup banner is tail-anchored like the streaming/thinking
                    UI above: while windowed away from the live tail (hasNewerMessages)
                    the in-memory bottom isn't the real bottom, so it stays gated --
                    otherwise a STARTING restart would paint the banner mid-history.
                  */}
                  <AgentStartupBanner
                    status={props.agentLifecycle?.agentStatus}
                    providerLabel={props.agentLifecycle?.providerLabel}
                    startupError={props.agentLifecycle?.startupError}
                    startupMessage={props.agentLifecycle?.startupMessage}
                    containerClass={styles.startupPanelInline}
                  />
                </Show>
              </div>
            </SelectionQuotePopover>
          </Show>
        </div>
        {/*
          History loading indicators: absolute OVERLAYS pinned to the top / bottom of
          the viewport, NOT in the scroll flow. In-flow they would shift the virtualized
          content by their height as fetching toggles -- a shift the anchor re-pin can't
          see -- bouncing a scrolled reader and wedging the load. See loadingOlderIndicator.

          Gated on scroll.stalledOlder() / stalledNewer(), not the raw fetching flags, so
          they show ONLY when the view is clamped against the loaded edge waiting on the
          fetch -- a background pre-fetch (the common case) stays silent.
        */}
        <Show when={scroll.stalledOlder()}>
          <div class={styles.loadingOlderIndicator}>
            <Spinner />
            Loading older messages...
          </div>
        </Show>
        <Show when={scroll.stalledNewer()}>
          <div class={styles.loadingNewerIndicator}>
            <Spinner />
            Loading newer messages...
          </div>
        </Show>
        {/*
          Hide the scroll-to-bottom button while the newer-loading indicator is up: both
          float at bottom-center, so the indicator takes the slot for the brief stall and
          the button reappears once the page lands (or the fetch times out).
        */}
        <Show when={!scroll.stalledNewer() && (!scroll.atBottom() || props.pagination?.hasNewerMessages)}>
          <button
            type="button"
            class={`outline icon ${styles.scrollToBottomButton}`}
            onClick={() => scroll.scrollToBottom()}
          >
            <Icon icon={ArrowDown} size="lg" />
          </button>
        </Show>
      </div>
      <ChatHiddenPremeasure
        candidates={premeasureCandidates()}
        contentWidthPx={effectiveContentWidth()}
        renderBubble={entry => renderMessageBubble(entry, { premeasureMode: true })}
        onMeasure={(id, height, heightKey, _measureDurationMs, settled) => {
          const accepted = virt.primeHeight(id, height, heightKey)
          const hasCommittedOrPendingHeight = accepted || virt.hasMeasuredHeight(id) || virt.hasPendingPremeasuredHeight(id)
          if (settled && hasCommittedOrPendingHeight) {
            setPendingPremeasureIds(ids => removeIdFromSet(ids, id))
            setUnsettledPremeasureKeys(keys => removeIdFromMap(keys, id))
          }
          else if (!settled && hasCommittedOrPendingHeight) {
            setUnsettledPremeasureKeys((keys) => {
              if (keys.has(id) && keys.get(id) === heightKey)
                return keys
              const next = new Map(keys)
              next.set(id, heightKey)
              return next
            })
          }
          return hasCommittedOrPendingHeight
        }}
      />
    </div>
  )
}
