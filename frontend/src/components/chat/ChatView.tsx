import type { Component } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { MessageBubbleHost } from './MessageBubble'
import type { ChatScrollState, PaginationCallbacks } from './useChatScroll'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import type { CommandStreamSegment, SpanMessageRevision } from '~/stores/chatTypes'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createEffect, createMemo, createSignal, For, Match, on, onCleanup, onMount, Show, Switch } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { Spinner } from '~/components/common/Spinner'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { formatChatQuote } from '~/lib/quoteUtils'
import { motion } from '~/styles/tokens'
import { AgentStartupBanner } from './AgentStartupBanner'
import { createClassifiedEntryCache, heightKeyForEntry } from './chatEntryCache'
import { ChatHiddenPremeasure } from './chatHiddenPremeasure'
import { createMessageUiState } from './chatMessageUiState'
import { createOrderedTailReveal } from './chatOrderedReveal'
import { createChatPremeasureBands } from './chatPremeasureBands'
import { kindScopedLayoutKey } from './chatRowGeometry'
import { createRowHeightPersistence } from './chatRowHeightPersistence'
import { createDelayedSet, createFlingSkeletonRegistry, createLingerSet } from './chatSkeletonCrossfade'
import { createStreamingTail } from './chatStreamingTail'
import * as styles from './ChatView.css'
import { computeOverscanPx, createViewportSizeObserver, measureSpaceToken, PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { MessageBubble } from './MessageBubble'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { assistantMessage } from './messageStyles.css'
import { expandedUiKeyFor, messageUiDefault } from './messageUiKeys'
import { ToolUseLayout } from './toolRenderers'
import { useChatScroll } from './useChatScroll'
import { sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'
import { ChatRowSkeleton } from './widgets/ChatRowSkeleton'
import { SpanLineGapBridges } from './widgets/SpanLineGapBridges'
import { SpanLines } from './widgets/SpanLines'
import { NO_SPAN_MARGIN } from './widgets/SpanLines.geometry'
import { ThinkingIndicator } from './widgets/ThinkingIndicator'

const SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS = 160
// How long an outgoing skeleton lingers (fading via rowSkeletonClosing) after
// its real content takes over, so the swap reads as a crossfade instead of a
// pop. The shared motion token keeps this unmount timer and the CSS fade
// duration in lockstep (see tokens.ts).
export const SKELETON_CROSSFADE_MS = motion.medium
// How long a row must stay hidden-pending-reveal before its loading skeleton is
// painted. A fast premeasure / re-measure (message expand-collapse, unified<->split
// diff-view switch, a freshly appended tail) settles well under this, so the row just
// fades in with no distracting shimmer; only a genuinely slow wait surfaces a skeleton
// as a loading affordance. See createDelayedSet.
export const SKELETON_SHOW_DELAY_MS = 500

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
   * Called on unmount with the final viewport scroll state, so a remount over the
   * same store (tile split/merge, workspace switch) can restore the reading
   * position through savedViewportScroll. Not called when the pane is hidden at
   * unmount (its tab-switch save must survive).
   */
  onSaveViewportScroll?: (state: ChatScrollState) => void
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

export const ChatView: Component<ChatViewProps> = (props) => {
  const prefs = usePreferences()

  // The virtualizer's live mounted-row set, assigned once `virt` is created below.
  // The UI-state cap reads it lazily (at toggle time, always after first render) to
  // protect on-screen rows from eviction -- mirrors how `flingSettle` is wired in
  // useChatScroll, so no forward reference to a not-yet-declared const.
  let mountedRowIds: ReadonlySet<string> = new Set()

  // Pin a row's top before a user toggle changes its height, so the toggled row stays
  // visually stationary instead of being scrolled by the viewport-midpoint re-pin.
  // Assigned once the scroll hook (which owns the anchor engine) is created below; a
  // no-op until then, and only invoked on user clicks (well after first render), so the
  // forward reference is never read before assignment -- same lazy-wire pattern as
  // mountedRowIds. See useChatScroll.anchorRowForResize.
  let anchorRowForResize: (messageId: string) => void = () => {}

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

  let contentRef: HTMLDivElement | undefined
  // The scroll container (also handed to scroll.attachListRef below). Read
  // non-reactively by isRowNearViewport at worker-dispatch time.
  let listEl: HTMLElement | undefined

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

  // DOM heights are width-sensitive, so the measured-height cache key folds the effective
  // layout width into EVERY row -- width genuinely rewraps every kind. The width fallback
  // must match hidden premeasure's fallback width: queued unsettled rows can remain mounted
  // while the pane reports width 0. Kind-SPECIFIC height inputs (diffView, expandAgentThoughts)
  // are NOT folded in here -- a global toggle of one of those would needlessly re-measure the
  // whole viewport. They are scoped per row instead (see kindScopedLayoutKey below).
  const globalEpochKey = createMemo(() =>
    JSON.stringify([
      effectiveContentWidth(),
      props.workingDir ?? '',
      props.homeDir ?? '',
    ]),
  )
  // The row's EFFECTIVE diff-view (its per-message override, else the global default). Read
  // per row only for diff-capable kinds, so a global unified<->split toggle re-keys only
  // those rows -- and skips a row whose local override already pins its value. (A per-message
  // override ALSO bumps that row's uiVersion, so it re-keys the row either way; reading the
  // override HERE is what lets a GLOBAL toggle leave an already-overridden row untouched.)
  const effectiveDiffView = (id: string): string => getLocalDiffView(id) ?? prefs.diffView()
  // The row's EFFECTIVE thinking-expand state (its per-message override, else the global
  // expandAgentThoughts default). Uses the SAME key + default resolvers the renderers use
  // (expandedUiKeyFor / messageUiDefault), so the cache key can't disagree with the render.
  const effectiveThinkingExpanded = (entry: ClassifiedEntry): boolean => {
    const key = expandedUiKeyFor(entry.category.kind, entry.msg.agentProvider)
    return getMessageUiBool(entry.msg.id, key) ?? messageUiDefault(key, { expandAgentThoughts: prefs.expandAgentThoughts() })
  }

  // Minimal per-row descriptors for the virtualizer. Unmeasured rows use only the
  // virtualizer's per-kind median estimate (keyed by `kind` below) until visible or
  // hidden DOM measurement commits a real height.
  const virtualItems = createMemo<VirtualItem[]>(
    () =>
      visibleEntries().map(e => ({
        id: e.msg.id,
        // Recorded onto a captured anchor so a trimmed-away row can be ordered against
        // the survivors for the nearest-survivor restore (scrollTopNearAnchor).
        seq: e.msg.seq,
        hasSpanLines: e.parsedSpanLines.length > 0,
        // Buckets the unmeasured-row height estimate by rendering kind (per-kind median),
        // so a short user row isn't over-estimated by a mean inflated with tall tool/code
        // rows. Reuses this entry's EXISTING classification -- the same category the row
        // renderer consumes below -- so the message is classified once, not twice.
        kind: e.category.kind,
        // The per-row measured-height cache key. heightKeyForEntry (chatEntryCache)
        // reads height-affecting freshness signals off the entry's own signature,
        // globalEpochKey covers width/dirs, and kindScopedLayoutKey folds in the
        // GLOBAL prefs (diffView / expandAgentThoughts) only for the kinds they can
        // resize -- so a global toggle re-measures just those, not the whole window.
        // Reading getUiVersion HERE subscribes this memo to the row's per-message UI
        // toggle, so stale premeasured heights are ignored the moment visible state
        // changes. DELIBERATELY EXCLUDED: live command-stream TEXT -- it grows on
        // every delta and is measured at the tail instead. The stream PRESENCE bit is
        // folded in (a classifier can change rendered structure from presence),
        // see EntryFreshness.
        heightKey: `${heightKeyForEntry(e, getUiVersion(e.msg.id))}|${globalEpochKey()}${kindScopedLayoutKey(
          e.category.kind,
          () => effectiveDiffView(e.msg.id),
          () => effectiveThinkingExpanded(e),
        )}|${rowChromeHeightKey(
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
  })
  // Point the UI-state cap's protect set at the virtualizer's live mounted rows
  // (a stable Set reference) now that `virt` exists.
  mountedRowIds = virt.mountedIds

  // Reload warm-start: hydrate persisted measured heights (keyed by height-
  // key digest, so content/width changes self-invalidate) and keep the
  // stored snapshot fresh. See chatRowHeightPersistence for the model.
  createRowHeightPersistence({
    storageId: () => props.agentId,
    virtualItems,
    virt,
  })

  const renderCacheKeyForEntry = (entry: ClassifiedEntry): string =>
    `${entry.msg.id}|${heightKeyForEntry(entry, getUiVersion(entry.msg.id))}`

  /**
   * Whether a row currently intersects the viewport plus half a screen of
   * slack — the priority band for worker dispatch (RenderContext.rowOffscreen):
   * rows outside it dispatch their markdown/highlight jobs at low priority.
   * Deliberately non-reactive: the worker gate re-reads it at each dispatch
   * opportunity, so it needs the CURRENT scroll position, not a subscription.
   */
  const isRowNearViewport = (id: string): boolean => {
    const el = listEl
    if (!el)
      return true // no DOM yet: don't deprioritize anything
    const index = virt.indexOfId(id)
    if (index < 0)
      return false // windowed away: nothing to paint
    const rowTop = virt.offsetOfIndex(index)
    const rowBottom = rowTop + virt.heightOfIndex(index)
    const slack = el.clientHeight / 2
    return rowBottom >= el.scrollTop - slack && rowTop <= el.scrollTop + el.clientHeight + slack
  }

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
    // Pin this row's top BEFORE the toggle changes its height, so it stays put instead of
    // being scrolled away by the geometry re-pin (which otherwise holds the viewport-
    // midpoint row). Both a diff-view switch and an expand/collapse resize the row.
    // Armed ONLY when the write will actually CHANGE state (mirroring the store's own
    // setIfChanged dedupe): a same-value write causes no resize, so arming would leave a
    // stale hold with nothing to release it until the next geometry commit yanks the
    // viewport back to the toggle-time line. And only for USER gestures: a programmatic
    // write (opts.programmatic -- e.g. a stream-start auto-expand re-asserted per chunk)
    // is not the reader's focus, so the default midpoint anchor -- which keeps what they
    // are READING stationary -- must win over pinning the written row.
    onSetLocalDiffView: (view) => {
      if (getLocalDiffView(entry.msg.id) !== view)
        anchorRowForResize(entry.msg.id)
      setLocalDiffView(entry.msg.id, view)
    },
    getMessageUiState: key => getMessageUiBool(entry.msg.id, key),
    setMessageUiState: (key, value, opts) => {
      if (!opts?.programmatic && getMessageUiBool(entry.msg.id, key) !== value)
        anchorRowForResize(entry.msg.id)
      setMessageUiBool(entry.msg.id, key, value)
    },
    getHeightDebug: () => virt.heightDebugOfId(entry.msg.id),
    renderCache: renderCacheStore.forRow(renderCacheKeyForEntry(entry)),
    syntaxHighlightingPaused,
    textSelectionActive,
    rowOffscreen: () => !isRowNearViewport(entry.msg.id),
  })

  // Derived lookups over the visible window, shared by the premeasure facade, the
  // streaming-tail machine, and the hide-until-measured logic below.
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

  // Premeasure bands: hidden-DOM premeasure of the ranged + look-ahead + idle warm-up
  // rows, fed into the coherence queue that de-dupes / collapses / settles them (see
  // createChatPremeasureBands). ChatView renders ChatHiddenPremeasure from
  // premeasureCandidates and hides in-range unmeasured rows via collapsedPremeasureIds.
  // The warm-up enable policy stays here (it reads ChatView-level scroll/stream state):
  // while the pane is visible, sized, and quiet. syntaxHighlightingPaused doubles as the
  // "scroll recently active" gate -- set on every scroll, cleared after the idle window.
  const premeasure = createChatPremeasureBands({
    visibleEntries,
    virtualItems,
    virt,
    contentWidth,
    visibleEntryById,
    warmupEnabled: () => (props.tabActive ?? true)
      && contentWidth() > 0
      && !props.streamingText
      && !syntaxHighlightingPaused(),
  })
  const { premeasureCandidates, collapsedPremeasureIds } = premeasure

  // Streaming-tail lifecycle: throttle the streaming markdown render, and when streaming
  // ends keep the in-flow bubble covering the persisted replacement row until it measures
  // (so the estimate->real swap doesn't blink). Owns the intricate stream->row handoff;
  // see createStreamingTail.
  const { renderedStreamHtml, streamingTailRender, streamReplacementTailId, isCoveredByInFlowTail }
    = createStreamingTail({
      streamingText: () => props.streamingText,
      streamingType: () => props.streamingType,
      hasNewerMessages: () => !!props.pagination?.hasNewerMessages,
      tailVisibleId: () => tailVisibleEntry()?.msg.id,
      hasMeasuredHeight: virt.hasMeasuredHeight,
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

  // The rendered window: only the rows in/near the viewport.
  const visibleSlice = createMemo(() => {
    const all = visibleEntries()
    const r = virt.range()
    return all.slice(r.start, r.end)
  })

  // An actively premeasured unknown-height row awaiting its OWN DOM height commit.
  // While true the row renders INVISIBLE (see rowHiddenUntilMeasured) and its
  // reserved slot is painted by the loading-skeleton overlay instead of blank
  // space. The live tail is included -- its unmeasured content would otherwise
  // overflow its estimated slot onto the trailing thinking indicator / streaming
  // UI -- EXCEPT the stream-covered tail, whose content is already painted by the
  // in-flow streaming bubble (a skeleton there would double-paint over live text).
  const rowAwaitingMeasurement = (id: string): boolean => (
    !virt.hasMeasuredHeight(id)
    && collapsedPremeasureIds().has(id)
    && streamReplacementTailId() !== id
  )

  // In-order reveal of an append burst: a measured tail row is held hidden until
  // every earlier still-loading row in its cohort has shown, so a later appended
  // message never pops in ahead of an earlier one -- even if it finishes measuring
  // first. Scoped to the tail cohort (see createOrderedTailReveal), so scrolling
  // back through already-loaded history is left untouched.
  const orderedRevealHeld = createOrderedTailReveal(
    () => visibleSlice().map(entry => entry.msg.id),
    rowAwaitingMeasurement,
  )

  // A row is hidden pending reveal when it is awaiting its OWN measurement OR being held
  // so an earlier appended sibling reveals first -- but NEVER while the in-flow streaming
  // bubble already covers it (the order gate can pick up a stream-replacement tail, whose
  // content the bubble paints; a skeleton there would double-paint over live text). The
  // row hides IMMEDIATELY (so it can't overflow its slot); its loading skeleton is
  // deferred separately (see skeletonSlice) so fast re-measures don't flash a shimmer.
  const rowHiddenPendingReveal = (id: string): boolean =>
    !isCoveredByInFlowTail(id) && (rowAwaitingMeasurement(id) || orderedRevealHeld().has(id))

  // Hide-until-measured, shared by the row itself and its gap-bridge overlay
  // entry: a premeasure-hidden (or order-held) row stays invisible until it is
  // ready to reveal, so a tall row can't paint beyond its estimated slot and
  // overlap what follows (a later row, or the in-flow tail UI). Rows with no
  // active premeasure stay visible so a zero attach read cannot hide content
  // forever.
  const rowHiddenUntilMeasured = (id: string): boolean => (
    isCoveredByInFlowTail(id) || rowHiddenPendingReveal(id)
  )

  // Fling-skeleton phases for the rendered rows, collected into one reactive set so the
  // gap-bridge overlay (rendered outside the rows) can see which rows are skeletons.
  const flingSkeletons = createFlingSkeletonRegistry(virt, SKELETON_CROSSFADE_MS)
  // Whether a row's span column is NOT currently painted, so its gap bridge must hide: a
  // premeasure-hidden row (rowHiddenUntilMeasured) OR a fling skeleton, whose inline
  // placeholder renders no SpanLines. Otherwise the bridge dangles as a rail segment above
  // a skeleton with nothing to connect to, until the real row upgrades in.
  const rowSpanColumnHidden = (id: string): boolean =>
    rowHiddenUntilMeasured(id) || flingSkeletons.skeletonIds().has(id)

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

  // Rows hidden pending reveal -- the CANDIDATES for a loading skeleton. Filter
  // preserves the stable entry references, so downstream <For>s only mount/unmount
  // on membership changes.
  const pendingRevealSlice = createMemo(() =>
    visibleSlice().filter(entry => rowHiddenPendingReveal(entry.msg.id)))

  // Only rows still hidden after SKELETON_SHOW_DELAY_MS actually paint a skeleton (see
  // createDelayedSet): a fast premeasure / re-measure (expand-collapse, diff-view switch,
  // tail append) reveals with a plain fade-in and no distracting shimmer; a slow wait
  // still surfaces a skeleton as a loading affordance. The row itself is hidden
  // immediately regardless (rowHiddenUntilMeasured) so it can never overflow its slot.
  const { delayedIds: skeletonIds } = createDelayedSet(
    () => pendingRevealSlice().map(entry => entry.msg.id),
    SKELETON_SHOW_DELAY_MS,
    virt.fastScrollActive,
  )
  // The rows currently PAINTING a skeleton overlay: the pending rows whose skeleton has
  // been shown -- either the wait exceeded SKELETON_SHOW_DELAY_MS, or a fast fling promoted
  // it immediately (see createDelayedSet's showImmediately). Fast scrolling into unmeasured
  // history skeletonises at once so it never flashes blank gaps; a quiet in-place re-measure
  // (expand-collapse, diff-view switch, tail append) waits out the delay so it reveals with a
  // plain fade-in and no distracting shimmer. Because the fling promotes into this same set
  // (not a separate bypass), a row shown mid-fling stays shown when the fling settles before
  // its delay would have fired, instead of flickering skeleton -> blank -> skeleton.
  const skeletonSlice = createMemo(() =>
    pendingRevealSlice().filter(entry => skeletonIds().has(entry.msg.id)))

  // Crossfade for the SHOWN skeletons: when a row leaves the skeleton set (its height
  // committed, the real row starts its opacity fade-in), its skeleton lingers for one
  // SKELETON_CROSSFADE_MS beat in a fading-out wrapper instead of popping away. A row
  // that re-enters cancels its linger — the live overlay covers it again. The linger
  // state machine (start-on-leave / cancel-on-re-enter / clear-on-cleanup) lives in
  // createLingerSet. Only rows that actually showed a skeleton linger; a fast reveal
  // (never skeletonised) just fades in with nothing to fade out.
  const { lingeringIds: lingeringSkeletonIds } = createLingerSet(
    () => skeletonSlice().map(entry => entry.msg.id),
    SKELETON_CROSSFADE_MS,
  )
  // Lingering ids resolved back to entries (stable references, so the fade-out
  // <For> keys correctly); a row windowed away mid-linger simply drops out.
  const lingeringSkeletonSlice = createMemo(() => {
    const byId = visibleEntryById()
    return [...lingeringSkeletonIds()]
      .map(id => byId.get(id))
      .filter((entry): entry is ClassifiedEntry => entry !== undefined)
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
    onSaveViewportScroll: state => props.onSaveViewportScroll?.(state),
  })
  // Now that the scroll hook (and its anchor engine) exists, point the toggle-time row
  // pin at it (see the `let` declaration above and buildMessageHost).
  anchorRowForResize = scroll.anchorRowForResize

  const pauseThen = <Args extends unknown[]>(handler: (...args: Args) => void): ((...args: Args) => void) =>
    (...args: Args) => {
      pauseSyntaxHighlightingForScroll()
      handler(...args)
    }

  const scrollHandlers = {
    onScroll: pauseThen(scroll.handlers.onScroll),
    onKeyDown: (event: KeyboardEvent) => {
      if (event.key === 'ArrowDown' || event.key === 'PageDown' || event.key === 'End' || event.key === ' '
        || event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home') {
        pauseSyntaxHighlightingForScroll()
      }
      scroll.handlers.onKeyDown(event)
    },
    onPointerDown: pauseThen(scroll.handlers.onPointerDown),
    onPointerMove: (event: PointerEvent) => {
      scroll.handlers.onPointerMove(event)
    },
    onPointerUp: pauseThen(scroll.handlers.onPointerUp),
    onPointerCancel: pauseThen(scroll.handlers.onPointerCancel),
  }

  // Wheel and touch listeners attach PASSIVE, imperatively: nothing in their
  // path calls preventDefault (createScrollInput's wheel policy only steers
  // pagination; the overscroll drag never cancels), but as JSX props they
  // register as non-passive listeners, which forces the compositor to block on
  // the main thread before starting the scroll — precisely the inputs where
  // scroll-start latency is felt. Solid's JSX prop syntax has no per-listener
  // options, so the container ref wires these. `scroll` (not cancelable —
  // passive is moot), keydown (NEEDS preventDefault: Home/End/Page keys own
  // their scroll), and the pointer handlers (never scroll-blocking) stay JSX
  // props above.
  const attachPassiveScrollListeners = (el: HTMLElement): void => {
    const passive = { passive: true } as const
    el.addEventListener('wheel', pauseThen(scroll.handlers.onWheel), passive)
    el.addEventListener('touchstart', pauseThen(scroll.handlers.onTouchStart), passive)
    el.addEventListener('touchmove', pauseThen(scroll.handlers.onTouchMove), passive)
    el.addEventListener('touchend', pauseThen(scroll.handlers.onTouchEnd), passive)
    el.addEventListener('touchcancel', pauseThen(scroll.handlers.onTouchCancel), passive)
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

  // One positioned skeleton slot, shared by the awaiting-measurement band and
  // the crossfade (lingering) band below -- they differ only by the closing
  // class, so the translateY-offset / reserved-height / seed wiring lives in one
  // place instead of two copies that could drift.
  const positionedRowSkeleton = (entry: ClassifiedEntry, closing = false) => (
    <div
      class={closing ? `${styles.virtualRow} ${styles.rowSkeletonClosing}` : styles.virtualRow}
      style={{ transform: `translateY(${virt.offsetOfId(entry.msg.id) ?? 0}px)` }}
    >
      <ChatRowSkeleton height={virt.heightOfId(entry.msg.id)} seed={entry.msg.id} />
    </div>
  )

  return (
    <div class={styles.container} data-testid="chat-container">
      <div class={styles.messageListWrapper}>
        <div
          ref={(el) => {
            listEl = el
            scroll.attachListRef(el)
            viewportSizeObserver.observe(el)
            attachPassiveScrollListeners(el)
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
                  {/* Inter-row rail segments, outside the paint-contained rows
                      (before the <For>, so rows paint over any overlap). Keyed
                      on the same stable entry references as the row <For>, so
                      geometry changes move anchors in place instead of
                      recreating the overlay DOM. */}
                  <SpanLineGapBridges
                    entries={visibleSlice()}
                    precedingEntry={visibleEntries()[virt.range().start - 1]}
                    topOf={id => virt.offsetOfId(id) ?? 0}
                    hiddenOf={rowSpanColumnHidden}
                  />
                  {/*
                    Loading skeletons: a premeasure-hidden row whose wait has
                    exceeded SKELETON_SHOW_DELAY_MS paints its reserved slot as
                    shimmer lines instead of blank space (a fast re-measure never
                    reaches here — it just fades in). Rendered OUTSIDE the rows — a
                    hidden row's opacity:0 would swallow any child — and positioned
                    by the same offsets, so they vanish seamlessly the moment the
                    real row's measurement commits and it fades in.
                  */}
                  <For each={skeletonSlice()}>
                    {entry => positionedRowSkeleton(entry)}
                  </For>
                  {/*
                    Crossfade tail: skeletons whose row just measured fade OUT
                    here while the real row's opacity fades in — the two
                    overlap for one beat, so the swap never pops.
                  */}
                  <For each={lingeringSkeletonSlice()}>
                    {entry => positionedRowSkeleton(entry, true)}
                  </For>
                  <For each={visibleSlice()}>
                    {(entry) => {
                      const { msg, parsedSpanLines } = entry
                      // Offset is resolved by the row's own id, not by
                      // range().start + localIndex(): the id is the stable,
                      // unique key into the offset map (seq is 0n for every
                      // optimistic local), so it can't transiently disagree with
                      // the slice bounds during a scroll/measure flush.
                      const top = () => virt.offsetOfId(msg.id) ?? 0
                      // Fling skeleton: a MEASURED row entering the window
                      // during a FAST user scroll mounts as line placeholders at
                      // its known height instead of paying full bubble
                      // construction on the scroll-critical path, then upgrades
                      // in place (skeleton -> crossfade -> real) once the scroll
                      // settles. The per-row phase machine lives in
                      // createRowUpgradePhase (see rowSkeletonUpgradeOverlay for
                      // the crossfade copy). trackRow also registers this row's phase
                      // in flingSkeletons.skeletonIds so the gap-bridge overlay hides
                      // this row's bridge while the skeleton shows.
                      const upgradePhase = flingSkeletons.trackRow(msg.id)

                      return (
                        <div
                          class={styles.virtualRow}
                          style={{
                            transform: `translateY(${top()}px)`,
                            // Absolute rows do not reserve flow height for
                            // siblings — see rowHiddenUntilMeasured for why an
                            // unmeasured premeasuring row stays invisible.
                            visibility: rowHiddenUntilMeasured(msg.id) ? 'hidden' : undefined,
                            opacity: rowHiddenUntilMeasured(msg.id) ? '0' : '1',
                          }}
                          data-seq={msg.seq.toString()}
                          ref={(el) => {
                            virt.attachRow(msg.id, el)
                            onCleanup(() => virt.detachRow(el))
                          }}
                        >
                          <Show
                            when={upgradePhase() !== 'skeleton'}
                            fallback={(
                              <ChatRowSkeleton
                                height={virt.heightOfId(msg.id)}
                                seed={msg.id}
                              />
                            )}
                          >
                            {(() => {
                              // Constructed lazily: while the skeleton shows,
                              // the bubble (markdown, tokens, toolbars) is
                              // never built for this row.
                              const bubble = renderMessageBubble(entry)
                              return (
                                <>
                                  <Show
                                    when={parsedSpanLines.length > 0}
                                    fallback={<div style={{ 'margin-left': `${NO_SPAN_MARGIN}px` }}>{bubble}</div>}
                                  >
                                    <div class={styles.messageRow}>
                                      <SpanLines
                                        lines={parsedSpanLines}
                                        spanOpener={!!msg.spanId}
                                      />
                                      <div class={styles.messageRowContent}>
                                        {bubble}
                                      </div>
                                    </div>
                                  </Show>
                                  <Show when={upgradePhase() === 'crossfade'}>
                                    <div class={`${styles.rowSkeletonUpgradeOverlay} ${styles.rowSkeletonClosing}`}>
                                      <ChatRowSkeleton
                                        height={virt.heightOfId(msg.id)}
                                        seed={msg.id}
                                      />
                                    </div>
                                  </Show>
                                </>
                              )
                            })()}
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
        onMeasure={premeasure.onMeasure}
      />
    </div>
  )
}
