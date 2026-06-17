import type { Component } from 'solid-js'
import type { MessageBubbleHost } from './MessageBubble'
import type { ChatScrollState, PaginationCallbacks } from './useChatScroll'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import type { CommandStreamSegment } from '~/stores/chatTypes'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createEffect, createMemo, createSignal, For, Match, on, onCleanup, onMount, Show, Switch } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { Spinner } from '~/components/common/Spinner'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { formatChatQuote } from '~/lib/quoteUtils'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { AgentStartupBanner } from './AgentStartupBanner'
import { createClassifiedEntryCache, estimateKeyForEntry } from './chatEntryCache'
import { buildEstimateEpoch, defaultHeightCtx } from './chatHeightEstimator'
import { createMessageUiState } from './chatMessageUiState'
import { createRowHeightInputs } from './chatRowHeightInputs'
import * as styles from './ChatView.css'
import { computeOverscanPx, createViewportSizeObserver, measureSpaceToken, PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { MessageBubble } from './MessageBubble'
import { assistantMessage } from './messageStyles.css'
import { ToolUseLayout } from './toolRenderers'
import { useChatScroll } from './useChatScroll'
import { DEFAULT_ESTIMATE_PX, sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'
import { SpanLines } from './widgets/SpanLines'
import { NO_SPAN_MARGIN } from './widgets/SpanLines.css'
import { ThinkingIndicator } from './widgets/ThinkingIndicator'

/** Imperative scroll API published by ChatView via `onScrollApiReady`. */
export interface ChatScrollApi {
  getScrollState: () => ChatScrollState | undefined
  forceScrollToBottom: () => void
  pageScroll: (direction: -1 | 1) => void
}

/**
 * The per-agent reactive lookups ChatView's renderers, entry cache, and height
 * estimator consult by spanId / message id. Bundled into one object (built once by the
 * host, TileRenderer) so the lookup cluster has a name and a single typed surface --
 * adding one no longer means a new prop plus three internal re-wiring sites. Every
 * member MUST read REACTIVELY (off the store) where noted, or an off-screen row freezes
 * at its first classification / estimate.
 */
export interface ChatMessageLookups {
  /** Look up the parsed tool_use message by spanId (for tool_use ↔ tool_result linking). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /**
   * The content version of the tool_use opener paired with a spanId (0 when none).
   * MUST read REACTIVELY (the store's getToolUseContentVersionBySpanId): a
   * tool_result sizes its diff from the opener's input, so an in-place opener body
   * change -- which bumps the OPENER's content version, not the result's -- must
   * re-classify and re-estimate the off-screen result. Folded into the entry
   * cache's freshness check and the per-row estimate key for tool_result rows.
   */
  getToolUseContentVersionBySpanId?: (spanId: string) => number
  /** Symmetric counterpart: look up the parsed tool_result message by spanId. */
  getToolResultParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
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
   * cache and the height estimate fold it in so such an update re-classifies and
   * re-estimates the row, which a same-seq proxy-preserving merge would otherwise hide.
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
   * and the row-height estimator read by spanId / message id. Bundled into one stable
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

  // Lifted per-message UI state (diff-view override + boolean flag map), keyed by
  // message id so a toggle survives <For> re-renders and a window trim. Owned by
  // createMessageUiState (its own tested unit); see that module for why it
  // deliberately outlives the windowed list and is cap-bounded instead of pruned.
  // The cap protects the currently-rendered rows so an on-screen row's choice is
  // never the eviction target.
  const { getLocalDiffView, setLocalDiffView, getMessageUiBool, setMessageUiBool, getUiVersion } = createMessageUiState({
    protectedIds: () => mountedRowIds,
  })

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
  const streamCoalescer = createRafCoalescer<string>(text =>
    setRenderedStreamHtml(renderMarkdown(text, true)),
  )

  createEffect(() => {
    const text = props.streamingText
    if (!text) {
      streamCoalescer.abort()
      setRenderedStreamHtml('')
      return
    }
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
    // A tool_result's height reads its paired tool_use; re-classify + re-estimate
    // the moment that opener is indexed so a late opener doesn't leave the row
    // frozen at its no-sibling size.
    hasToolUseSiblingBySpanId: spanId => props.lookups?.getToolUseParsedBySpanId?.(spanId) !== undefined,
    // A tool_result reads its opener's content to size the diff; the opener is a
    // DIFFERENT message, so its in-place body change bumps the opener's version (not
    // the result's). Reading it here re-classifies + re-estimates the result row the
    // moment its opener changes, instead of leaving it frozen at the pre-change size.
    toolUseSiblingContentVersionBySpanId: spanId => props.lookups?.getToolUseContentVersionBySpanId?.(spanId) ?? 0,
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

  // Inner content width of the message list, the wrap width the estimator needs.
  // One signal for all rows, bucketed to 8px so scrollbar/sub-pixel jitter doesn't
  // storm the geom recompute (only unmeasured/off-screen rows re-estimate on it).
  //
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

  // Shared estimate context; only the width varies at runtime. PRE_MEASURE_WIDTH_PX
  // covers the pre-measurement frame so prose wrap math never divides by ~0.
  const heightCtx = createMemo(() => defaultHeightCtx(contentWidth() || PRE_MEASURE_WIDTH_PX))

  // The ClassifiedEntry -> HeightInput -> estimate pipeline (chatRowHeightInputs).
  // The prefs are passed as ACCESSORS so buildRowInput reads them inside the
  // virtualizer's per-row estimate thunk -- tracking the current global prefs --
  // rather than capturing them at construction.
  const rowHeightInputs = createRowHeightInputs({
    getEntry: entries.getEntry,
    getMessageUiBool,
    getLocalDiffView,
    expandAgentThoughts: () => prefs.expandAgentThoughts(),
    diffView: () => prefs.diffView(),
    getToolUseParsedBySpanId: spanId => props.lookups?.getToolUseParsedBySpanId?.(spanId),
    workingDir: () => props.workingDir,
    homeDir: () => props.homeDir,
    heightCtx,
  })

  // Minimal per-row descriptors for the virtualizer. `features` is a LAZY thunk
  // carrying the analytical estimator's pre-mount input (kind + content metrics +
  // state): the virtualizer invokes it only for a row that needs an estimate
  // (never-measured / cache-miss), so the per-row parse + diff-geometry build is
  // skipped for the measured-and-cached majority of the window on each recompute.
  const virtualItems = createMemo<VirtualItem[]>(
    () =>
      visibleEntries().map(e => ({
        id: e.msg.id,
        // Recorded onto a captured anchor so a trimmed-away row can be ordered against
        // the survivors for the nearest-survivor restore (scrollTopNearAnchor).
        seq: e.msg.seq,
        hasSpanLines: e.parsedSpanLines.length > 0,
        features: () => rowHeightInputs.buildRowInput(e),
        // The per-row estimate-cache key. estimateKeyForEntry (chatEntryCache) reads
        // the height-affecting freshness signals off the entry's own signature, so the
        // freshness->key bridge lives next to EntryFreshness. Reading getUiVersion HERE
        // subscribes this memo to the row's per-message UI toggle, so the key changes
        // the moment its UI state does. DELIBERATELY EXCLUDED: live command-stream
        // (streaming) TEXT -- it grows on EVERY delta and lives in the command-stream
        // store (not msg.content), so folding it in would re-run the per-row estimate
        // per chunk; streaming rows measure at the tail instead. The stream PRESENCE
        // bit IS folded in (a classifier can size a row from presence), see EntryFreshness.
        estimateKey: estimateKeyForEntry(e, getUiVersion(e.msg.id)),
      })),
    [],
    // Suppress the geom rebuild + scroll re-pin when a recompute leaves the offset
    // map identical (same id/hasSpanLines/estimateKey sequence) -- see sameVirtualItems.
    { equals: sameVirtualItems },
  )

  // Overscan scales with the live viewport height (clamped) -- see computeOverscanPx.
  const overscanPx = () => computeOverscanPx(viewportHeight())

  const virt = useChatVirtualizer({
    items: virtualItems,
    gapSmallPx,
    gapLargePx,
    overscanPx,
    // `.total` of the breakdown is the estimated height; the full breakdown is also
    // exposed via estimateBreakdown below for the raw-JSON debug surface (one
    // estimateRowHeight call, not two). A featureless row seeds the default.
    estimate: item => rowHeightInputs.estimateItemBreakdown(item)?.total ?? DEFAULT_ESTIMATE_PX,
    // The estimate epoch is EVERY input to the estimate other than the row
    // identity: the bucketed content width AND the row's resolved UI state. A
    // per-message toggle only affects an on-screen (measured) row, but an
    // UNMEASURED off-screen row's state falls back to two GLOBAL prefs --
    // expandAgentThoughts (thinking rows) and diffView (diff rows) -- so flipping
    // either changes the analytical estimate of every off-screen row. The epoch
    // must therefore fold in those prefs: keyed on width alone, the estimate cache
    // would hand back each off-screen row's stale pre-toggle height and the offset
    // map (and the scroll anchor) would drift until each row scrolled into view.
    // buildEstimateEpoch composes these into one primitive string so any change busts
    // the cache wholesale.
    estimateEpoch: () => buildEstimateEpoch({
      contentWidth: contentWidth(),
      expandAgentThoughts: prefs.expandAgentThoughts(),
      diffView: prefs.diffView(),
    }),
    onFirstMeasure: rowHeightInputs.logHeightEstimateMiss,
    // Debug-only: surfaces the full estimate breakdown via heightDebugOfId for the
    // "Copy Raw JSON" geometry field. Not used for the offset map.
    estimateBreakdown: rowHeightInputs.estimateItemBreakdown,
  })
  // Point the UI-state cap's protect set at the virtualizer's live mounted rows
  // (a stable Set reference) now that `virt` exists.
  mountedRowIds = virt.mountedIds

  /**
   * The per-row bindings a MessageBubble needs from ChatView, bundled into one
   * typed object: the agent-stable lookups (hostLookups) spread with the bindings
   * that genuinely vary per message -- the row's live command stream (keyed on its
   * spanId), its lifted diff-view / UI state (keyed on its id), and its height-debug
   * readout (keyed on its id, off `virt`). Built per row in the <For>; the split
   * documents exactly which bindings are message-scoped. Defined after `virt` so the
   * getHeightDebug binding can read it without a use-before-define.
   */
  const buildMessageHost = (msg: AgentChatMessage): MessageBubbleHost => ({
    ...hostLookups(),
    commandStream: () => props.lookups?.getCommandStreamBySpanId?.(msg.spanId),
    localDiffView: getLocalDiffView(msg.id),
    onSetLocalDiffView: view => setLocalDiffView(msg.id, view),
    getMessageUiState: key => getMessageUiBool(msg.id, key),
    setMessageUiState: (key, value) => setMessageUiBool(msg.id, key, value),
    getHeightDebug: () => virt.heightDebugOfId(msg.id),
  })

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
          {...scroll.handlers}
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
              containerRef={contentRef}
              onQuote={text => props.onQuote?.(formatChatQuote(text))}
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
                    {(entry) => {
                      const { msg, parsed, category, parsedSpanLines } = entry
                      // Offset is resolved by the row's own id, not by
                      // range().start + localIndex(): the id is the stable,
                      // unique key into the offset map (seq is 0n for every
                      // optimistic local), so it can't transiently disagree with
                      // the slice bounds during a scroll/measure flush.
                      const top = () => virt.offsetOfId(msg.id) ?? 0
                      const bubble = (
                        <MessageBubble
                          message={msg}
                          parsed={parsed}
                          category={category}
                          error={props.messageErrors?.[msg.id]}
                          pendingLabel={props.messagePendingLabels?.[msg.id]}
                          onRetry={() => props.onRetryMessage?.(msg.id)}
                          onDelete={() => props.onDeleteMessage?.(msg.id)}
                          workingDir={props.workingDir}
                          homeDir={props.homeDir}
                          onReply={props.onReply}
                          host={buildMessageHost(msg)}
                        />
                      )

                      return (
                        <div
                          class={styles.virtualRow}
                          style={{ transform: `translateY(${top()}px)` }}
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
                              <SpanLines lines={parsedSpanLines} spanOpener={!!msg.spanId} />
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
                  <Show when={props.streamingText}>
                    <Show
                      when={props.streamingType === 'plan'}
                      fallback={(
                        <div class={assistantMessage}>
                          {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                          <div class={markdownContent} innerHTML={renderedStreamHtml()} />
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
                          <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderedStreamHtml()} />
                        </>
                      </ToolUseLayout>
                    </Show>
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
    </div>
  )
}
